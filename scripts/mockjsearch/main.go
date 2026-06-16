// Command mockjsearch serves a realistic JSearch API response on any path, so
// the scraping pipeline can be exercised without reaching the live RapidAPI
// gateway. For local testing only:
//
//	go run ./scripts/mockjsearch &           # listens on :8099
//	go run ./cmd/test-scraper -url http://localhost:8099/search -key x
package main

import (
	"log"
	"net/http"
)

const sample = `{
  "status": "OK",
  "data": [
    {
      "job_id": "gh-001",
      "employer_name": "Wix",
      "job_title": "Senior Full Stack Developer",
      "job_description": "Build large-scale web apps with React and Node.js.",
      "job_apply_link": "https://boards.greenhouse.io/wix/jobs/1001",
      "job_city": "Tel Aviv", "job_state": "", "job_country": "IL",
      "job_employment_type": "FULLTIME", "job_is_remote": false,
      "job_posted_at_datetime_utc": "2026-06-14T09:00:00.000Z",
      "job_min_salary": 35000, "job_max_salary": 48000, "job_salary_currency": "ILS",
      "job_required_skills": ["React", "Node.js", "TypeScript"]
    },
    {
      "job_id": "lv-002",
      "employer_name": "Monday.com",
      "job_title": "Full Stack Engineer",
      "job_description": "Work across the stack on our work-OS platform.",
      "job_apply_link": "https://jobs.lever.co/monday/abc-123",
      "job_city": "Tel Aviv", "job_state": "", "job_country": "IL",
      "job_employment_type": "FULLTIME", "job_is_remote": true,
      "job_posted_at_datetime_utc": "2026-06-15T12:00:00.000Z",
      "job_required_skills": ["Go", "React", "PostgreSQL", "AWS"]
    },
    {
      "job_id": "li-003",
      "employer_name": "Fiverr",
      "job_title": "Software Engineer, Backend",
      "job_description": "Scale our marketplace backend.",
      "job_apply_link": "https://www.linkedin.com/jobs/view/3003",
      "job_city": "Remote", "job_state": "", "job_country": "IL",
      "job_employment_type": "FULLTIME", "job_is_remote": true,
      "job_posted_at_datetime_utc": "2026-06-16T08:00:00.000Z",
      "job_required_skills": ["Go", "Docker", "Kubernetes"]
    }
  ]
}`

func main() {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		log.Printf("mock JSearch hit: %s %s", r.Method, r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sample))
	})
	log.Println("mock JSearch listening on http://localhost:8099")
	log.Fatal(http.ListenAndServe("localhost:8099", nil))
}
