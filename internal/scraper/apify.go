package scraper

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/models"
	"github.com/yalimeiri/job-agent/internal/retry"
)

const apifyBaseURL = "https://api.apify.com/v2"

// ApifyScraper runs a configured Apify Actor synchronously (e.g. a LinkedIn or
// Indeed jobs scraper) and maps its dataset items into Jobs. Actor output
// schemas vary between actors, so each Job field is resolved from a small list
// of common candidate keys, with an optional per-field override in config
// (field_title, field_company, field_location, field_apply_url, field_url,
// field_description, field_external_id, field_remote, field_job_type).
//
// Downstream routing needs no changes: whatever ApplyURL an actor reports is
// run through the existing platform detector, so a job whose real apply link
// resolves to Greenhouse/Lever/etc. is auto-applied, while anything that stays
// on LinkedIn/Indeed's own apply flow is correctly routed to notify-only.
type ApifyScraper struct {
	name      string
	token     string
	actorID   string // normalized "user~actor"
	inputJSON []byte
	baseURL   string // overridable for tests; defaults to apifyBaseURL

	fieldOverrides map[string]string // e.g. "title" -> "jobTitle"

	client *http.Client
	log    *slog.Logger
}

// newApifyScraper is the Factory for "apify" sources.
func newApifyScraper(cfg config.SourceConfig, deps Deps) (Scraper, error) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}

	token := cfg.Extra["api_token"]
	if token == "" {
		return nil, fmt.Errorf("apify source %q: extra.api_token is required", cfg.Name)
	}
	actorID := strings.ReplaceAll(strings.Trim(cfg.Extra["actor_id"], "/"), "/", "~")
	if actorID == "" {
		return nil, fmt.Errorf("apify source %q: extra.actor_id is required", cfg.Name)
	}
	inputJSON := cfg.Extra["input_json"]
	if inputJSON == "" {
		inputJSON = "{}"
	}
	if !json.Valid([]byte(inputJSON)) {
		return nil, fmt.Errorf("apify source %q: extra.input_json is not valid JSON", cfg.Name)
	}

	timeout := 120 * time.Second
	if v := cfg.Extra["timeout_secs"]; v != "" {
		if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
			timeout = time.Duration(secs) * time.Second
		}
	}

	overrides := map[string]string{}
	for _, k := range []string{"title", "company", "location", "apply_url", "url", "description", "external_id", "remote", "job_type"} {
		if v := cfg.Extra["field_"+k]; v != "" {
			overrides[k] = v
		}
	}

	base := deps.HTTPClient
	if base == nil {
		base = &http.Client{}
	}
	// Actor runs can take a while; give the client enough headroom beyond the
	// requested actor timeout.
	client := &http.Client{Timeout: timeout + 30*time.Second, Transport: base.Transport}

	baseURL := apifyBaseURL
	if v := cfg.Extra["base_url"]; v != "" {
		baseURL = v
	}

	return &ApifyScraper{
		name:           cfg.Name,
		token:          token,
		actorID:        actorID,
		inputJSON:      []byte(inputJSON),
		baseURL:        baseURL,
		fieldOverrides: overrides,
		client:         client,
		log:            deps.Logger.With("scraper", cfg.Name, "type", "apify"),
	}, nil
}

// Name implements Scraper.
func (s *ApifyScraper) Name() string { return s.name }

// Scrape runs the actor and maps its dataset items into Jobs.
func (s *ApifyScraper) Scrape(ctx context.Context) (*models.ScrapeResult, error) {
	result := &models.ScrapeResult{Source: s.name}

	items, err := s.runActor(ctx)
	if err != nil {
		result.Err = err
		return result, err
	}

	seen := map[string]bool{}
	for _, item := range items {
		job := s.mapJob(item)
		if job == nil || job.ExternalID == "" || seen[job.ID] {
			continue
		}
		seen[job.ID] = true
		result.Jobs = append(result.Jobs, job)
	}
	s.log.Info("scrape complete", "items", len(items), "jobs", len(result.Jobs))
	return result, nil
}

// runActor calls Apify's run-sync-get-dataset-items endpoint, which runs the
// actor and returns its output dataset in one call (bounded by Apify's own
// sync-run time limit).
func (s *ApifyScraper) runActor(ctx context.Context) ([]map[string]any, error) {
	endpoint := fmt.Sprintf("%s/acts/%s/run-sync-get-dataset-items?clean=true", s.baseURL, s.actorID)

	var items []map[string]any
	err := retry.Do(ctx, retry.DefaultPolicy(), func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(s.inputJSON))
		if err != nil {
			return retry.Permanent(err)
		}
		req.Header.Set("Authorization", "Bearer "+s.token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := s.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		switch {
		case resp.StatusCode == http.StatusOK:
			if err := json.Unmarshal(body, &items); err != nil {
				return retry.Permanent(fmt.Errorf("decode dataset items: %w", err))
			}
			return nil
		case resp.StatusCode == http.StatusTooManyRequests:
			return retry.RetryAfter{Err: fmt.Errorf("apify 429"), Delay: parseRetryAfter(resp)}
		case resp.StatusCode >= 500:
			return fmt.Errorf("apify %d: %s", resp.StatusCode, snippet(body))
		default:
			return retry.Permanent(fmt.Errorf("apify %d: %s", resp.StatusCode, snippet(body)))
		}
	})
	return items, err
}

// mapJob converts one Apify dataset item into a Job. Returns nil if no title
// could be resolved (the item is assumed not to be a job record).
func (s *ApifyScraper) mapJob(item map[string]any) *models.Job {
	title := s.field(item, "title", "title", "jobTitle", "position")
	if title == "" {
		return nil
	}
	company := s.field(item, "company", "companyName", "company", "employerName")
	location := s.field(item, "location", "location", "jobLocation", "city")
	applyURL := s.field(item, "apply_url", "applyUrl", "apply_url", "applyLink")
	pageURL := s.field(item, "url", "link", "url", "jobUrl", "postUrl")
	if applyURL == "" {
		applyURL = pageURL // no separate apply link: the listing page IS the apply flow
	}
	if pageURL == "" {
		pageURL = applyURL
	}
	description := s.field(item, "description", "descriptionText", "description", "jobDescription")
	externalID := s.field(item, "external_id", "id", "jobId", "trackingId", "refId")
	if externalID == "" {
		externalID = externalIDFor(applyURL, title)
	}
	jobType := s.field(item, "job_type", "employmentType", "jobType")

	remote := false
	if r := s.field(item, "remote", "isRemote", "remote"); r != "" {
		remote = strings.EqualFold(r, "true")
	} else {
		remote = strings.Contains(strings.ToLower(location), "remote")
	}

	return &models.Job{
		ID:          jobID(s.name, externalID),
		ExternalID:  externalID,
		Source:      s.name,
		Title:       title,
		Company:     company,
		Location:    location,
		Description: description,
		URL:         pageURL,
		ApplyURL:    applyURL,
		JobType:     jobType,
		Remote:      remote,
		Status:      models.StatusNew,
	}
}

// field resolves a Job field from item: a configured override key (if any)
// takes priority, then the given default candidate keys are tried in order.
func (s *ApifyScraper) field(item map[string]any, overrideName string, defaults ...string) string {
	keys := defaults
	if override, ok := s.fieldOverrides[overrideName]; ok {
		keys = append([]string{override}, defaults...)
	}
	for _, k := range keys {
		v, ok := item[k]
		if !ok {
			continue
		}
		switch t := v.(type) {
		case string:
			if t != "" {
				return t
			}
		case bool:
			return strconv.FormatBool(t)
		case float64:
			return strconv.FormatFloat(t, 'f', -1, 64)
		}
	}
	return ""
}

// Ensure ApifyScraper satisfies Scraper.
var _ Scraper = (*ApifyScraper)(nil)
