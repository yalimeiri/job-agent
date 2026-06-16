package agent

import (
	"context"
	"sync"
	"testing"

	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/models"
	"github.com/yalimeiri/job-agent/internal/scraper"
)

// ---------------------------------------------------------------------------
// Fakes
// ---------------------------------------------------------------------------

type fakeStore struct {
	mu      sync.Mutex
	jobs    map[string]*models.Job
	profile map[string]string
}

func newFakeStore() *fakeStore {
	return &fakeStore{jobs: map[string]*models.Job{}, profile: map[string]string{"email": "you@example.com"}}
}

func (s *fakeStore) SaveJob(_ context.Context, j *models.Job) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.jobs[j.ID]; ok {
		return false, nil
	}
	cp := *j
	s.jobs[j.ID] = &cp
	return true, nil
}
func (s *fakeStore) UpdateJobStatus(_ context.Context, id string, st models.JobStatus) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := s.jobs[id]; j != nil {
		j.Status = st
	}
	return nil
}
func (s *fakeStore) UpdateMatch(_ context.Context, id string, score float64, reason, cl string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if j := s.jobs[id]; j != nil {
		j.MatchScore, j.MatchReason, j.CoverLetter = score, reason, cl
	}
	return nil
}
func (s *fakeStore) ListByStatus(_ context.Context, st models.JobStatus) ([]*models.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []*models.Job
	for _, j := range s.jobs {
		if j.Status == st {
			cp := *j
			out = append(out, &cp)
		}
	}
	return out, nil
}
func (s *fakeStore) HasSeen(_ context.Context, ext, src string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.jobs[src+":"+ext]
	return ok, nil
}
func (s *fakeStore) DetectPlatform(_ context.Context, applyURL string) (*models.Platform, error) {
	if applyURL == "https://boards.greenhouse.io/acme/jobs/1" {
		return &models.Platform{ID: 1, Name: "greenhouse", Enabled: true}, nil
	}
	if applyURL == "https://linkedin.com/jobs/view/1" {
		return &models.Platform{ID: 7, Name: "linkedin", Enabled: false}, nil
	}
	return nil, nil // unknown
}
func (s *fakeStore) SetJobPlatform(context.Context, string, int) error { return nil }
func (s *fakeStore) GetFieldMappings(context.Context, int, int) ([]models.FieldMapping, error) {
	return nil, nil
}
func (s *fakeStore) GetPlatformSteps(context.Context, int) ([]models.PlatformStep, error) {
	return nil, nil
}
func (s *fakeStore) GetCommonQuestions(context.Context, *int) ([]models.CommonQuestion, error) {
	return nil, nil
}
func (s *fakeStore) GetProfile(context.Context) (map[string]string, error) { return s.profile, nil }
func (s *fakeStore) UpsertProfileField(context.Context, string, string, string) error {
	return nil
}
func (s *fakeStore) LogApplicationAction(context.Context, models.ApplicationLogEntry) error {
	return nil
}
func (s *fakeStore) UpdateSelectorHealth(context.Context, int, bool) error { return nil }
func (s *fakeStore) GetStats(context.Context) (map[models.JobStatus]int, error) {
	return nil, nil
}
func (s *fakeStore) Close() error { return nil }

type fakeScraper struct {
	name string
	jobs []*models.Job
}

func (f *fakeScraper) Name() string { return f.name }
func (f *fakeScraper) Scrape(context.Context) (*models.ScrapeResult, error) {
	return &models.ScrapeResult{Source: f.name, Jobs: f.jobs}, nil
}

// fakeMatcher scores by job title lookup.
type fakeMatcher struct{ scores map[string]float64 }

func (m *fakeMatcher) Match(_ context.Context, job *models.Job, _ config.ProfileConfig) (*models.MatchResult, error) {
	return &models.MatchResult{Score: m.scores[job.Title], Reasoning: "test", CoverLetter: "letter"}, nil
}

type fakeNotifier struct {
	mu      sync.Mutex
	reports int
	alerts  []string
}

func (n *fakeNotifier) SendReport(context.Context, *models.CycleReport) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.reports++
	return nil
}
func (n *fakeNotifier) SendAlert(_ context.Context, msg string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.alerts = append(n.alerts, msg)
	return nil
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func testConfig() *config.Config {
	return &config.Config{
		Agent:    config.AgentConfig{MaxConcurrent: 2, DryRun: true},
		Profile:  config.ProfileConfig{FullName: "Jane", Email: "j@x.com"},
		Matching: config.MatchingConfig{MinScore: 0.6, AutoApplyAbove: 0.85},
	}
}

func TestRunOnceScrapeMatchRoutes(t *testing.T) {
	st := newFakeStore()
	scr := &fakeScraper{name: "src", jobs: []*models.Job{
		{ID: "src:1", ExternalID: "1", Source: "src", Title: "Great", ApplyURL: "https://boards.greenhouse.io/acme/jobs/1", Status: models.StatusNew},
		{ID: "src:2", ExternalID: "2", Source: "src", Title: "Meh", ApplyURL: "https://x", Status: models.StatusNew},
		{ID: "src:3", ExternalID: "3", Source: "src", Title: "Hot", ApplyURL: "https://linkedin.com/jobs/view/1", Status: models.StatusNew},
	}}
	mt := &fakeMatcher{scores: map[string]float64{"Great": 0.9, "Meh": 0.3, "Hot": 0.95}}
	nt := &fakeNotifier{}

	a := New(testConfig(), Deps{Store: st, Scrapers: []scraper.Scraper{scr}, Matcher: mt, Notifier: nt})

	report, err := a.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("RunOnce() = %v", err)
	}

	if report.Scraped != 3 || report.NewJobs != 3 {
		t.Errorf("scraped=%d new=%d, want 3/3", report.Scraped, report.NewJobs)
	}
	// Great (0.9) and Hot (0.95) >= 0.6 => matched; Meh rejected.
	if report.Matched != 2 || report.Rejected != 1 {
		t.Errorf("matched=%d rejected=%d, want 2/1", report.Matched, report.Rejected)
	}
	// Two jobs >= highScoreAlert (0.9) => two alerts during match phase
	// (greenhouse 0.9, linkedin 0.95), plus notify-only alerts in apply phase.
	if len(nt.alerts) < 2 {
		t.Errorf("expected >=2 alerts, got %d", len(nt.alerts))
	}
	// Dry-run: nothing applied; both auto-apply candidates skipped.
	if report.Applied != 0 {
		t.Errorf("applied=%d, want 0 in dry run", report.Applied)
	}
	if nt.reports != 1 {
		t.Errorf("reports=%d, want 1", nt.reports)
	}
}

func TestApplyPhaseLiveAppliesGreenhouse(t *testing.T) {
	st := newFakeStore()
	// Pre-seed a matched greenhouse job above the auto-apply threshold.
	st.jobs["src:1"] = &models.Job{
		ID: "src:1", ExternalID: "1", Source: "src", Title: "Great",
		ApplyURL: "https://boards.greenhouse.io/acme/jobs/1",
		Status:   models.StatusMatched, MatchScore: 0.9,
	}
	ap := &fakeApplier{result: &models.ApplicationResult{Success: true, ScreenshotPath: "shot.png"}}
	nt := &fakeNotifier{}

	cfg := testConfig()
	cfg.Agent.DryRun = false
	a := New(cfg, Deps{Store: st, Matcher: &fakeMatcher{}, Applier: ap, Notifier: nt})

	report := &models.CycleReport{ScraperError: map[string]string{}}
	a.applyPhase(context.Background(), report)

	if report.Applied != 1 {
		t.Fatalf("applied=%d, want 1", report.Applied)
	}
	if st.jobs["src:1"].Status != models.StatusApplied {
		t.Errorf("status=%q, want applied", st.jobs["src:1"].Status)
	}
	if !ap.called {
		t.Error("applier was not called")
	}
}

type fakeApplier struct {
	called bool
	result *models.ApplicationResult
}

func (f *fakeApplier) Apply(context.Context, *models.Job, map[string]string, string) (*models.ApplicationResult, error) {
	f.called = true
	return f.result, nil
}
func (f *fakeApplier) Close() error { return nil }
