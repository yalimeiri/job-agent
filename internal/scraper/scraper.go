// Package scraper discovers job listings from external sources. Each source is
// a Scraper; a Registry maps a source "type" to a factory so the agent can
// build the configured scrapers from YAML without knowing concrete types.
package scraper

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/models"
	"github.com/yalimeiri/job-agent/internal/ratelimit"
)

// Scraper fetches job listings from a source.
type Scraper interface {
	Name() string
	Scrape(ctx context.Context) (*models.ScrapeResult, error)
}

// Deps are the shared dependencies injected into every scraper.
type Deps struct {
	HTTPClient *http.Client
	Limiters   *ratelimit.Registry
	Logger     *slog.Logger
}

// Factory builds a Scraper from its source config and shared deps.
type Factory func(cfg config.SourceConfig, deps Deps) (Scraper, error)

// Registry maps source type -> Factory.
type Registry struct {
	factories map[string]Factory
}

// NewRegistry returns a registry pre-populated with the built-in scrapers.
func NewRegistry() *Registry {
	r := &Registry{factories: map[string]Factory{}}
	r.Register("jsearch", newJSearchScraper)
	r.Register("career_page", newCareerPageScraper)
	return r
}

// Register adds (or overrides) a factory for a source type.
func (r *Registry) Register(typ string, f Factory) {
	r.factories[typ] = f
}

// Build constructs a Scraper for the given source config.
func (r *Registry) Build(cfg config.SourceConfig, deps Deps) (Scraper, error) {
	f, ok := r.factories[cfg.Type]
	if !ok {
		return nil, fmt.Errorf("unknown scraper type %q for source %q", cfg.Type, cfg.Name)
	}
	if deps.HTTPClient == nil {
		deps.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	return f(cfg, deps)
}

// BuildAll constructs scrapers for every enabled source.
func (r *Registry) BuildAll(cfg *config.Config, deps Deps) ([]Scraper, error) {
	var out []Scraper
	for _, s := range cfg.EnabledSources() {
		sc, err := r.Build(s, deps)
		if err != nil {
			return nil, err
		}
		out = append(out, sc)
	}
	return out, nil
}

// jobID builds the deterministic primary key for a job.
func jobID(source, externalID string) string {
	return source + ":" + externalID
}
