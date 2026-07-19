package scraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yalimeiri/job-agent/internal/config"
)

// sampleApifyItems mimics a LinkedIn-jobs-style Apify actor's dataset output:
// one item whose real application lives on Greenhouse (auto-appliable), one
// with no separate apply link (stays on LinkedIn, notify-only downstream).
const sampleApifyItems = `[
  {
    "id": "3812345678",
    "title": "Junior Software Engineer",
    "companyName": "Wix",
    "location": "Tel Aviv, Israel",
    "link": "https://www.linkedin.com/jobs/view/3812345678",
    "applyUrl": "https://boards.greenhouse.io/wix/jobs/2001",
    "descriptionText": "Join our team building scalable web apps.",
    "employmentType": "Full-time"
  },
  {
    "id": "3812345679",
    "title": "Student Software Engineer",
    "companyName": "Monday.com",
    "location": "Remote",
    "link": "https://www.linkedin.com/jobs/view/3812345679",
    "descriptionText": "Part time roles for students.",
    "employmentType": "Part-time"
  },
  {
    "title": "",
    "companyName": "NoTitleCorp"
  }
]`

func TestApifyScrapeMapsFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/acts/bebity~linkedin-jobs-scraper/run-sync-get-dataset-items") {
			t.Errorf("unexpected path %q (actor_id slash should become tilde)", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer apify-test-token" {
			t.Errorf("missing/invalid bearer auth: %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleApifyItems))
	}))
	defer srv.Close()

	scr, err := newApifyScraper(config.SourceConfig{
		Name: "apify_linkedin", Type: "apify", Enabled: true,
		Extra: map[string]string{
			"api_token":  "apify-test-token",
			"actor_id":   "bebity/linkedin-jobs-scraper", // slash form; should normalize
			"input_json": `{"title":"Junior Software Engineer","location":"Israel"}`,
			"base_url":   srv.URL,
		},
	}, Deps{HTTPClient: srv.Client()})
	if err != nil {
		t.Fatal(err)
	}

	res, err := scr.Scrape(context.Background())
	if err != nil {
		t.Fatalf("Scrape() = %v", err)
	}
	// The empty-title record must be skipped.
	if len(res.Jobs) != 2 {
		t.Fatalf("got %d jobs, want 2", len(res.Jobs))
	}

	wix := res.Jobs[0]
	if wix.Title != "Junior Software Engineer" || wix.Company != "Wix" {
		t.Errorf("job[0] = %+v", wix)
	}
	// Real apply link resolves to Greenhouse -> downstream auto-apply eligible.
	if wix.ApplyURL != "https://boards.greenhouse.io/wix/jobs/2001" {
		t.Errorf("job[0] ApplyURL = %q, want greenhouse link", wix.ApplyURL)
	}
	if wix.Remote {
		t.Error("job[0] should not be marked remote")
	}

	monday := res.Jobs[1]
	// No separate apply link: falls back to the LinkedIn page URL itself,
	// so downstream platform detection correctly routes it to notify-only.
	if monday.ApplyURL != "https://www.linkedin.com/jobs/view/3812345679" {
		t.Errorf("job[1] ApplyURL = %q, want linkedin fallback", monday.ApplyURL)
	}
	if !monday.Remote {
		t.Error("job[1] location 'Remote' should mark Remote=true")
	}
}

func TestApifyFieldOverrideWithFallback(t *testing.T) {
	// Simulates an actor with a non-standard title key ("jobTitle"), overridden
	// via config, while still falling back to the default "title" key for
	// items that happen to use it.
	items := `[
      {"jobTitle": "Backend Intern", "companyName": "Acme", "id": "1", "link": "https://x/1"},
      {"title": "Frontend Intern", "companyName": "Acme", "id": "2", "link": "https://x/2"}
    ]`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(items))
	}))
	defer srv.Close()

	scr, err := newApifyScraper(config.SourceConfig{
		Name: "apify_x", Type: "apify",
		Extra: map[string]string{
			"api_token": "tok", "actor_id": "acme~jobs", "input_json": "{}",
			"base_url": srv.URL, "field_title": "jobTitle",
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
	if res.Jobs[0].Title != "Backend Intern" {
		t.Errorf("override field not used: %+v", res.Jobs[0])
	}
	if res.Jobs[1].Title != "Frontend Intern" {
		t.Errorf("fallback to default field failed: %+v", res.Jobs[1])
	}
}

func TestNewApifyScraperValidation(t *testing.T) {
	tests := []struct {
		name  string
		extra map[string]string
	}{
		{"missing token", map[string]string{"actor_id": "a~b"}},
		{"missing actor_id", map[string]string{"api_token": "t"}},
		{"invalid input_json", map[string]string{"api_token": "t", "actor_id": "a~b", "input_json": "{not json"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newApifyScraper(config.SourceConfig{Name: "s", Extra: tt.extra}, Deps{})
			if err == nil {
				t.Fatal("expected error")
			}
		})
	}
}
