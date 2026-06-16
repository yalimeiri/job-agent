package scraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yalimeiri/job-agent/internal/config"
)

const sampleHTML = `<html><body>
  <div class="job-listing">
    <a class="job-title" href="/jobs/1">Backend Engineer</a>
    <span class="job-location">Tel Aviv</span>
  </div>
  <div class="job-listing">
    <a class="job-title" href="https://other.example.com/jobs/2">Frontend Engineer</a>
    <span class="job-location">Remote</span>
  </div>
</body></html>`

func TestCareerPageScrape(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ua := r.Header.Get("User-Agent"); ua == "" {
			t.Error("expected User-Agent header")
		}
		_, _ = w.Write([]byte(sampleHTML))
	}))
	defer srv.Close()

	scr, err := newCareerPageScraper(config.SourceConfig{
		Name: "acme", Type: "career_page",
		Extra: map[string]string{
			"url":               srv.URL,
			"job_selector":      ".job-listing",
			"title_selector":    ".job-title",
			"location_selector": ".job-location",
			"link_selector":     "a",
			"base_url":          "https://acme.example.com",
		},
	}, Deps{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}

	res, err := scr.Scrape(context.Background())
	if err != nil {
		t.Fatalf("Scrape() = %v", err)
	}
	if len(res.Jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(res.Jobs))
	}

	// Relative link resolved against base_url.
	if got := res.Jobs[0].URL; got != "https://acme.example.com/jobs/1" {
		t.Errorf("job[0] URL = %q", got)
	}
	if res.Jobs[0].Title != "Backend Engineer" || res.Jobs[0].Location != "Tel Aviv" {
		t.Errorf("job[0] title/loc = %q / %q", res.Jobs[0].Title, res.Jobs[0].Location)
	}
	// Absolute link left intact.
	if got := res.Jobs[1].URL; got != "https://other.example.com/jobs/2" {
		t.Errorf("job[1] URL = %q", got)
	}
}
