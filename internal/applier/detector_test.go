package applier

import (
	"testing"

	"github.com/yalimeiri/job-agent/internal/models"
)

// platforms mirrors the seeded url_patterns for detection tests.
var platforms = []models.Platform{
	{ID: 1, Name: "greenhouse", URLPattern: `boards\.greenhouse\.io|job-boards\.greenhouse\.io`, Enabled: true},
	{ID: 2, Name: "lever", URLPattern: `jobs\.lever\.co`, Enabled: true},
	{ID: 3, Name: "workday", URLPattern: `myworkdayjobs\.com`, Enabled: true},
	{ID: 4, Name: "ashby", URLPattern: `jobs\.ashbyhq\.com`, Enabled: true},
	{ID: 7, Name: "linkedin", URLPattern: `linkedin\.com/jobs`, Enabled: false},
}

func TestMatchPlatform(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string // platform name, "" for no match
	}{
		{"greenhouse boards", "https://boards.greenhouse.io/acme/jobs/123", "greenhouse"},
		{"greenhouse job-boards", "https://job-boards.greenhouse.io/acme/jobs/9", "greenhouse"},
		{"lever", "https://jobs.lever.co/acme/abc-def", "lever"},
		{"workday", "https://acme.wd1.myworkdayjobs.com/careers/job/123", "workday"},
		{"ashby", "https://jobs.ashbyhq.com/acme/123", "ashby"},
		{"linkedin", "https://www.linkedin.com/jobs/view/123", "linkedin"},
		{"unknown", "https://careers.acme.com/apply/42", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MatchPlatform(platforms, tt.url)
			if err != nil {
				t.Fatalf("MatchPlatform() error = %v", err)
			}
			if tt.want == "" {
				if got != nil {
					t.Fatalf("expected no match, got %q", got.Name)
				}
				return
			}
			if got == nil || got.Name != tt.want {
				t.Fatalf("MatchPlatform() = %v, want %q", got, tt.want)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		p    *models.Platform
		want PlatformClass
	}{
		{"nil is unknown", nil, ClassUnknown},
		{"linkedin notify only", &models.Platform{Name: "linkedin", Enabled: false}, ClassNotifyOnly},
		{"disabled notify only", &models.Platform{Name: "greenhouse", Enabled: false}, ClassNotifyOnly},
		{"enabled automate", &models.Platform{Name: "greenhouse", Enabled: true}, ClassAutomate},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.p); got != tt.want {
				t.Errorf("Classify() = %v, want %v", got, tt.want)
			}
		})
	}
}
