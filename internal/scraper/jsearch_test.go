package scraper

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/ratelimit"
)

const sampleJSearch = `{
  "status": "OK",
  "data": [
    {
      "job_id": "abc123",
      "employer_name": "Acme Corp",
      "job_title": "Full Stack Developer",
      "job_description": "Build things.",
      "job_apply_link": "https://boards.greenhouse.io/acme/jobs/1",
      "job_city": "Tel Aviv",
      "job_state": "",
      "job_country": "IL",
      "job_employment_type": "FULLTIME",
      "job_is_remote": true,
      "job_posted_at_datetime_utc": "2026-06-10T08:00:00.000Z",
      "job_min_salary": 120000,
      "job_max_salary": 160000,
      "job_salary_currency": "USD",
      "job_required_skills": ["Go", "React"]
    },
    {
      "job_id": "",
      "employer_name": "ShouldSkip",
      "job_title": "No ID"
    }
  ]
}`

func TestJSearchParse(t *testing.T) {
	s := &JSearchScraper{name: "jsearch_test"}
	jobs, err := s.parse([]byte(sampleJSearch))
	if err != nil {
		t.Fatalf("parse() = %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("got %d jobs, want 1 (empty job_id should be skipped)", len(jobs))
	}
	j := jobs[0]
	if j.ID != "jsearch_test:abc123" {
		t.Errorf("ID = %q", j.ID)
	}
	if j.Title != "Full Stack Developer" || j.Company != "Acme Corp" {
		t.Errorf("title/company = %q/%q", j.Title, j.Company)
	}
	if j.Location != "Tel Aviv, IL" {
		t.Errorf("location = %q, want 'Tel Aviv, IL'", j.Location)
	}
	if !j.Remote {
		t.Error("remote should be true")
	}
	if j.Requirements != "Go, React" {
		t.Errorf("requirements = %q", j.Requirements)
	}
	if j.Salary != "USD 120000-160000" {
		t.Errorf("salary = %q", j.Salary)
	}
	if j.PostedAt == nil {
		t.Error("posted_at should be parsed")
	}
}

func TestJSearchParseBadStatus(t *testing.T) {
	s := &JSearchScraper{name: "x"}
	if _, err := s.parse([]byte(`{"status":"ERROR","data":[]}`)); err == nil {
		t.Fatal("expected error on non-OK status")
	}
}

func TestJSearchScrapeEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-RapidAPI-Key") != "test-key" {
			t.Errorf("missing api key header")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(sampleJSearch))
	}))
	defer srv.Close()

	deps := Deps{
		HTTPClient: srv.Client(),
		Limiters:   ratelimit.NewRegistry(),
	}
	scr, err := newJSearchScraper(config.SourceConfig{
		Name: "jsearch_test", Type: "jsearch", Enabled: true,
		Extra: map[string]string{"api_key": "test-key", "queries": "go dev|go dev"},
	}, deps)
	if err != nil {
		t.Fatal(err)
	}
	js := scr.(*JSearchScraper)
	js.baseURL = srv.URL // point at the test server

	res, err := js.Scrape(context.Background())
	if err != nil {
		t.Fatalf("Scrape() = %v", err)
	}
	// Two identical queries return the same job; dedup should yield exactly one.
	if len(res.Jobs) != 1 {
		t.Fatalf("got %d jobs after dedup, want 1", len(res.Jobs))
	}
}

func TestFormatSalary(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	tests := []struct {
		min, max *float64
		cur      string
		want     string
	}{
		{nil, nil, "USD", ""},
		{f(100), f(200), "USD", "USD 100-200"},
		{f(100), nil, "EUR", "EUR 100+"},
		{nil, f(200), "", "up to USD 200"},
	}
	for _, tt := range tests {
		if got := formatSalary(tt.min, tt.max, tt.cur); got != tt.want {
			t.Errorf("formatSalary = %q, want %q", got, tt.want)
		}
	}
}
