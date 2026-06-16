package scraper

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/models"
	"github.com/yalimeiri/job-agent/internal/ratelimit"
	"github.com/yalimeiri/job-agent/internal/retry"
)

const (
	jsearchHost    = "jsearch.p.rapidapi.com"
	jsearchBaseURL = "https://jsearch.p.rapidapi.com/search"
	// jsearchMonthlyBudget is the RapidAPI free-tier quota.
	jsearchMonthlyBudget = 200
)

// JSearchScraper queries the JSearch RapidAPI (aggregates LinkedIn, Indeed,
// Glassdoor, ZipRecruiter) for one or more search queries.
type JSearchScraper struct {
	name       string
	apiKey     string
	queries    []string
	datePosted string
	remoteOnly bool
	numPages   int
	baseURL    string // overridable for tests; defaults to jsearchBaseURL
	client     *http.Client
	limiter    *ratelimit.Limiter
	log        *slog.Logger
}

// newJSearchScraper is the Factory for "jsearch" sources.
func newJSearchScraper(cfg config.SourceConfig, deps Deps) (Scraper, error) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Limiters == nil {
		deps.Limiters = ratelimit.NewRegistry()
	}
	apiKey := cfg.Extra["api_key"]
	if apiKey == "" {
		return nil, fmt.Errorf("jsearch source %q: extra.api_key is required", cfg.Name)
	}

	rawQueries := cfg.Extra["queries"]
	if rawQueries == "" {
		return nil, fmt.Errorf("jsearch source %q: extra.queries is required", cfg.Name)
	}
	var queries []string
	for _, q := range strings.Split(rawQueries, "|") {
		if q = strings.TrimSpace(q); q != "" {
			queries = append(queries, q)
		}
	}

	datePosted := cfg.Extra["date_posted"]
	if datePosted == "" {
		datePosted = "3days"
	}
	numPages := 1
	if v := cfg.Extra["num_pages"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			numPages = n
		}
	}

	// One limiter per source name, enforcing the monthly budget.
	lim := deps.Limiters.Get(cfg.Name)
	if lim == nil {
		lim = ratelimit.PerMonth(cfg.Name, jsearchMonthlyBudget)
		deps.Limiters.Register(cfg.Name, lim)
	}

	return &JSearchScraper{
		name:       cfg.Name,
		apiKey:     apiKey,
		queries:    queries,
		datePosted: datePosted,
		remoteOnly: strings.EqualFold(cfg.Extra["remote_only"], "true"),
		numPages:   numPages,
		baseURL:    jsearchBaseURL,
		client:     deps.HTTPClient,
		limiter:    lim,
		log:        deps.Logger.With("scraper", cfg.Name, "type", "jsearch"),
	}, nil
}

// Name implements Scraper.
func (s *JSearchScraper) Name() string { return s.name }

// Scrape runs every configured query and returns the de-duplicated jobs.
func (s *JSearchScraper) Scrape(ctx context.Context) (*models.ScrapeResult, error) {
	result := &models.ScrapeResult{Source: s.name}
	seen := map[string]bool{}

	for _, q := range s.queries {
		jobs, err := s.searchQuery(ctx, q)
		if err != nil {
			// Surface the first failure but keep results gathered so far.
			s.log.Error("query failed", "query", q, "err", err)
			if result.Err == nil {
				result.Err = err
			}
			continue
		}
		for _, j := range jobs {
			if seen[j.ID] {
				continue
			}
			seen[j.ID] = true
			result.Jobs = append(result.Jobs, j)
		}
		s.log.Info("query complete", "query", q, "jobs", len(jobs))
	}
	return result, result.Err
}

func (s *JSearchScraper) searchQuery(ctx context.Context, query string) ([]*models.Job, error) {
	if s.limiter != nil {
		if err := s.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("rate limit: %w", err)
		}
	}

	u, _ := url.Parse(s.baseURL)
	qp := u.Query()
	qp.Set("query", query)
	qp.Set("page", "1")
	qp.Set("num_pages", strconv.Itoa(s.numPages))
	qp.Set("date_posted", s.datePosted)
	if s.remoteOnly {
		qp.Set("remote_jobs_only", "true")
	}
	u.RawQuery = qp.Encode()

	var body []byte
	err := retry.Do(ctx, retry.DefaultPolicy(), func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return retry.Permanent(err)
		}
		req.Header.Set("X-RapidAPI-Key", s.apiKey)
		req.Header.Set("X-RapidAPI-Host", jsearchHost)

		resp, err := s.client.Do(req)
		if err != nil {
			return err // network error: retryable
		}
		defer resp.Body.Close()

		b, readErr := io.ReadAll(resp.Body)
		switch {
		case resp.StatusCode == http.StatusOK:
			if readErr != nil {
				return readErr
			}
			body = b
			return nil
		case resp.StatusCode == http.StatusTooManyRequests:
			return retry.RetryAfter{Err: fmt.Errorf("jsearch 429"), Delay: parseRetryAfter(resp)}
		case resp.StatusCode >= 500:
			return fmt.Errorf("jsearch %d: %s", resp.StatusCode, snippet(b))
		default:
			return retry.Permanent(fmt.Errorf("jsearch %d: %s", resp.StatusCode, snippet(b)))
		}
	})
	if err != nil {
		return nil, err
	}

	return s.parse(body)
}

// jsearchResponse mirrors the relevant fields of the JSearch API response.
type jsearchResponse struct {
	Status string `json:"status"`
	Data   []struct {
		JobID                  string   `json:"job_id"`
		EmployerName           string   `json:"employer_name"`
		JobTitle               string   `json:"job_title"`
		JobDescription         string   `json:"job_description"`
		JobApplyLink           string   `json:"job_apply_link"`
		JobCity                string   `json:"job_city"`
		JobState               string   `json:"job_state"`
		JobCountry             string   `json:"job_country"`
		JobEmploymentType      string   `json:"job_employment_type"`
		JobIsRemote            bool     `json:"job_is_remote"`
		JobPostedAtDatetimeUTC string   `json:"job_posted_at_datetime_utc"`
		JobMinSalary           *float64 `json:"job_min_salary"`
		JobMaxSalary           *float64 `json:"job_max_salary"`
		JobSalaryCurrency      string   `json:"job_salary_currency"`
		JobRequiredSkills      []string `json:"job_required_skills"`
		JobHighlights          struct {
			Qualifications   []string `json:"Qualifications"`
			Responsibilities []string `json:"Responsibilities"`
		} `json:"job_highlights"`
	} `json:"data"`
}

func (s *JSearchScraper) parse(body []byte) ([]*models.Job, error) {
	var r jsearchResponse
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("decode jsearch response: %w", err)
	}
	if r.Status != "OK" {
		return nil, fmt.Errorf("jsearch status %q", r.Status)
	}

	jobs := make([]*models.Job, 0, len(r.Data))
	for _, d := range r.Data {
		if d.JobID == "" {
			continue
		}
		requirements := strings.Join(d.JobRequiredSkills, ", ")
		if requirements == "" {
			requirements = strings.Join(d.JobHighlights.Qualifications, "\n")
		}

		var postedAt *time.Time
		if d.JobPostedAtDatetimeUTC != "" {
			if t, err := time.Parse(time.RFC3339, d.JobPostedAtDatetimeUTC); err == nil {
				postedAt = &t
			}
		}

		jobs = append(jobs, &models.Job{
			ID:           jobID(s.name, d.JobID),
			ExternalID:   d.JobID,
			Source:       s.name,
			Title:        d.JobTitle,
			Company:      d.EmployerName,
			Location:     joinLocation(d.JobCity, d.JobState, d.JobCountry),
			Description:  d.JobDescription,
			Requirements: requirements,
			URL:          d.JobApplyLink,
			ApplyURL:     d.JobApplyLink,
			Salary:       formatSalary(d.JobMinSalary, d.JobMaxSalary, d.JobSalaryCurrency),
			JobType:      d.JobEmploymentType,
			Remote:       d.JobIsRemote,
			PostedAt:     postedAt,
			Status:       models.StatusNew,
		})
	}
	return jobs, nil
}

func joinLocation(parts ...string) string {
	var nonEmpty []string
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, ", ")
}

func formatSalary(min, max *float64, currency string) string {
	if min == nil && max == nil {
		return ""
	}
	if currency == "" {
		currency = "USD"
	}
	switch {
	case min != nil && max != nil:
		return fmt.Sprintf("%s %.0f-%.0f", currency, *min, *max)
	case min != nil:
		return fmt.Sprintf("%s %.0f+", currency, *min)
	default:
		return fmt.Sprintf("up to %s %.0f", currency, *max)
	}
}

func parseRetryAfter(resp *http.Response) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

func snippet(b []byte) string {
	const max = 200
	s := strings.TrimSpace(string(b))
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
