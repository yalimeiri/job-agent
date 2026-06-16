//go:build integration

package store

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/yalimeiri/job-agent/internal/models"
)

// startPostgres spins up a throwaway Postgres container, applies all migrations
// and returns a connected Store plus a cleanup func.
func startPostgres(t *testing.T) (*Postgres, string) {
	t.Helper()
	ctx := context.Background()

	ctr, err := postgres.RunContainer(ctx,
		testcontainers.WithImage("postgres:16-alpine"),
		postgres.WithDatabase("jobagent"),
		postgres.WithUsername("jobagent"),
		postgres.WithPassword("jobagent"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(60*time.Second)),
	)
	if err != nil {
		t.Fatalf("start postgres: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(ctx) })

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}

	migrationsDir, err := filepath.Abs(filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	if err := Migrate(dsn, migrationsDir); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	st, err := New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st, dsn
}

func TestIntegrationJobLifecycle(t *testing.T) {
	st, _ := startPostgres(t)
	ctx := context.Background()

	job := &models.Job{
		ID: "src:1", ExternalID: "1", Source: "src",
		Title: "Full Stack Developer", Company: "Acme",
		ApplyURL: "https://boards.greenhouse.io/acme/jobs/1",
		Status:   models.StatusNew,
	}

	// SaveJob is idempotent on (external_id, source).
	isNew, err := st.SaveJob(ctx, job)
	if err != nil || !isNew {
		t.Fatalf("SaveJob() isNew=%v err=%v, want true/nil", isNew, err)
	}
	isNew, _ = st.SaveJob(ctx, job)
	if isNew {
		t.Fatal("duplicate SaveJob should report isNew=false")
	}

	seen, err := st.HasSeen(ctx, "1", "src")
	if err != nil || !seen {
		t.Fatalf("HasSeen() = %v, %v; want true", seen, err)
	}

	if err := st.UpdateMatch(ctx, job.ID, 0.92, "great fit", "Dear team..."); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateJobStatus(ctx, job.ID, models.StatusMatched); err != nil {
		t.Fatal(err)
	}

	matched, err := st.ListByStatus(ctx, models.StatusMatched)
	if err != nil || len(matched) != 1 {
		t.Fatalf("ListByStatus(matched) = %d jobs, %v; want 1", len(matched), err)
	}
	if matched[0].MatchScore != 0.92 || matched[0].CoverLetter == "" {
		t.Errorf("match not persisted: %+v", matched[0])
	}

	stats, err := st.GetStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats[models.StatusMatched] != 1 {
		t.Errorf("stats[matched] = %d, want 1", stats[models.StatusMatched])
	}
}

func TestIntegrationSeedDataAndDetection(t *testing.T) {
	st, _ := startPostgres(t)
	ctx := context.Background()

	// Platform detection from seeded url_patterns.
	pl, err := st.DetectPlatform(ctx, "https://boards.greenhouse.io/acme/jobs/9")
	if err != nil || pl == nil {
		t.Fatalf("DetectPlatform() = %v, %v; want greenhouse", pl, err)
	}
	if pl.Name != "greenhouse" {
		t.Fatalf("detected %q, want greenhouse", pl.Name)
	}

	// LinkedIn is seeded but disabled (notify-only).
	li, _ := st.DetectPlatform(ctx, "https://www.linkedin.com/jobs/view/1")
	if li == nil || li.Enabled {
		t.Fatalf("linkedin should be detected and disabled, got %+v", li)
	}

	// Seeded Greenhouse field mappings.
	mappings, err := st.GetFieldMappings(ctx, pl.ID, 1)
	if err != nil || len(mappings) == 0 {
		t.Fatalf("GetFieldMappings() = %d, %v; want > 0", len(mappings), err)
	}

	// Common questions: global ones should be returned.
	qs, err := st.GetCommonQuestions(ctx, &pl.ID)
	if err != nil || len(qs) == 0 {
		t.Fatalf("GetCommonQuestions() = %d, %v; want > 0", len(qs), err)
	}

	// Profile seed present.
	profile, err := st.GetProfile(ctx)
	if err != nil || profile["email"] == "" {
		t.Fatalf("GetProfile() = %v, %v", profile, err)
	}

	// Selector health upsert + audit log require a real job row (FK).
	job := &models.Job{ID: "src:2", ExternalID: "2", Source: "src", Title: "T", Company: "C",
		ApplyURL: "https://boards.greenhouse.io/x", Status: models.StatusNew}
	if _, err := st.SaveJob(ctx, job); err != nil {
		t.Fatal(err)
	}
	if err := st.UpdateSelectorHealth(ctx, mappings[0].ID, false); err != nil {
		t.Fatalf("UpdateSelectorHealth() = %v", err)
	}
	if err := st.UpdateSelectorHealth(ctx, mappings[0].ID, true); err != nil {
		t.Fatalf("UpdateSelectorHealth() = %v", err)
	}
	pid := pl.ID
	if err := st.LogApplicationAction(ctx, models.ApplicationLogEntry{
		JobID: job.ID, PlatformID: &pid, Action: "navigate", Success: true,
	}); err != nil {
		t.Fatalf("LogApplicationAction() = %v", err)
	}
}
