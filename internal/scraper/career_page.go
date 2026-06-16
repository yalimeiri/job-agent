package scraper

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/models"
	"github.com/yalimeiri/job-agent/internal/retry"
)

const browserUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
	"(KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"

// CareerPageScraper is a generic HTML scraper driven by CSS selectors from
// config. It fetches a listing page and extracts one job per matched element.
type CareerPageScraper struct {
	name string

	pageURL          string
	jobSelector      string
	titleSelector    string
	locationSelector string
	linkSelector     string
	baseURL          string

	client *http.Client
	log    *slog.Logger
}

// newCareerPageScraper is the Factory for "career_page" sources.
func newCareerPageScraper(cfg config.SourceConfig, deps Deps) (Scraper, error) {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	pageURL := cfg.Extra["url"]
	if pageURL == "" {
		return nil, fmt.Errorf("career_page source %q: extra.url is required", cfg.Name)
	}
	jobSel := cfg.Extra["job_selector"]
	if jobSel == "" {
		return nil, fmt.Errorf("career_page source %q: extra.job_selector is required", cfg.Name)
	}
	base := cfg.Extra["base_url"]
	if base == "" {
		if u, err := url.Parse(pageURL); err == nil {
			base = u.Scheme + "://" + u.Host
		}
	}

	return &CareerPageScraper{
		name:             cfg.Name,
		pageURL:          pageURL,
		jobSelector:      jobSel,
		titleSelector:    cfg.Extra["title_selector"],
		locationSelector: cfg.Extra["location_selector"],
		linkSelector:     cfg.Extra["link_selector"],
		baseURL:          base,
		client:           deps.HTTPClient,
		log:              deps.Logger.With("scraper", cfg.Name, "type", "career_page"),
	}, nil
}

// Name implements Scraper.
func (s *CareerPageScraper) Name() string { return s.name }

// Scrape fetches and parses the configured listing page.
func (s *CareerPageScraper) Scrape(ctx context.Context) (*models.ScrapeResult, error) {
	result := &models.ScrapeResult{Source: s.name}

	doc, err := s.fetch(ctx, s.pageURL)
	if err != nil {
		result.Err = err
		return result, err
	}

	doc.Find(s.jobSelector).Each(func(_ int, sel *goquery.Selection) {
		title := s.text(sel, s.titleSelector)
		if title == "" {
			return
		}
		link := s.link(sel)
		location := s.text(sel, s.locationSelector)

		ext := externalIDFor(link, title)
		result.Jobs = append(result.Jobs, &models.Job{
			ID:         jobID(s.name, ext),
			ExternalID: ext,
			Source:     s.name,
			Title:      title,
			Company:    s.name,
			Location:   location,
			URL:        link,
			ApplyURL:   link,
			Status:     models.StatusNew,
		})
	})

	s.log.Info("scrape complete", "jobs", len(result.Jobs))
	return result, nil
}

func (s *CareerPageScraper) fetch(ctx context.Context, target string) (*goquery.Document, error) {
	var doc *goquery.Document
	err := retry.Do(ctx, retry.DefaultPolicy(), func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return retry.Permanent(err)
		}
		req.Header.Set("User-Agent", browserUA)
		req.Header.Set("Accept", "text/html,application/xhtml+xml")

		resp, err := s.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		switch {
		case resp.StatusCode == http.StatusOK:
		case resp.StatusCode == http.StatusTooManyRequests:
			return retry.RetryAfter{Err: fmt.Errorf("career page 429"), Delay: parseRetryAfter(resp)}
		case resp.StatusCode >= 500:
			return fmt.Errorf("career page %d", resp.StatusCode)
		default:
			return retry.Permanent(fmt.Errorf("career page %d", resp.StatusCode))
		}

		d, err := goquery.NewDocumentFromReader(resp.Body)
		if err != nil {
			return retry.Permanent(err)
		}
		doc = d
		return nil
	})
	return doc, err
}

// text returns the trimmed text for selector within sel, or sel's own text if
// selector is empty.
func (s *CareerPageScraper) text(sel *goquery.Selection, selector string) string {
	if selector == "" {
		return strings.TrimSpace(sel.Text())
	}
	return strings.TrimSpace(sel.Find(selector).First().Text())
}

// link extracts and absolutises the job link.
func (s *CareerPageScraper) link(sel *goquery.Selection) string {
	var node *goquery.Selection
	if s.linkSelector != "" {
		node = sel.Find(s.linkSelector).First()
	} else if sel.Is("a") {
		node = sel
	} else {
		node = sel.Find("a").First()
	}
	href, ok := node.Attr("href")
	if !ok {
		return ""
	}
	return s.absolute(strings.TrimSpace(href))
}

func (s *CareerPageScraper) absolute(href string) string {
	if href == "" || strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") {
		return href
	}
	base, err := url.Parse(s.baseURL)
	if err != nil {
		return href
	}
	ref, err := url.Parse(href)
	if err != nil {
		return href
	}
	return base.ResolveReference(ref).String()
}

// externalIDFor derives a stable id from the link (preferred) or title.
func externalIDFor(link, title string) string {
	key := link
	if key == "" {
		key = title
	}
	sum := sha1.Sum([]byte(key))
	return hex.EncodeToString(sum[:])[:16]
}
