// Package agent is the orchestrator. It runs the cycle
// scrape -> dedup -> match -> apply -> notify, either once or on an interval.
package agent

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/yalimeiri/job-agent/internal/applier"
	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/matcher"
	"github.com/yalimeiri/job-agent/internal/models"
	"github.com/yalimeiri/job-agent/internal/notifier"
	"github.com/yalimeiri/job-agent/internal/scraper"
	"github.com/yalimeiri/job-agent/internal/store"
)

// highScoreAlert is the score above which an individual job triggers a Telegram
// alert during the match phase.
const highScoreAlert = 0.9

// Deps are the collaborators the agent orchestrates.
type Deps struct {
	Store    store.Store
	Scrapers []scraper.Scraper
	Matcher  matcher.Matcher
	Applier  applier.Applier // may be nil to disable the apply phase
	Notifier notifier.Notifier
	Logger   *slog.Logger
}

// Agent wires the cycle together.
type Agent struct {
	cfg  *config.Config
	deps Deps
	log  *slog.Logger
}

// New constructs an Agent.
func New(cfg *config.Config, deps Deps) *Agent {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Notifier == nil {
		deps.Notifier = notifier.Nop{}
	}
	return &Agent{cfg: cfg, deps: deps, log: deps.Logger.With("component", "agent")}
}

// Run executes the cycle immediately and then on every interval tick until ctx
// is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	a.log.Info("agent starting", "interval", a.cfg.Agent.Interval, "dry_run", a.cfg.Agent.DryRun)

	if _, err := a.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		a.log.Error("cycle failed", "err", err)
	}

	ticker := time.NewTicker(a.cfg.Agent.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			a.log.Info("agent stopping")
			return ctx.Err()
		case <-ticker.C:
			if _, err := a.RunOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
				a.log.Error("cycle failed", "err", err)
			}
		}
	}
}

// RunOnce executes a single scrape -> match -> apply -> notify cycle.
func (a *Agent) RunOnce(ctx context.Context) (*models.CycleReport, error) {
	report := &models.CycleReport{
		StartedAt:    time.Now(),
		DryRun:       a.cfg.Agent.DryRun,
		ScraperError: map[string]string{},
	}
	a.log.Info("cycle start")

	a.scrapePhase(ctx, report)
	if err := ctx.Err(); err != nil {
		return report, err
	}
	a.matchPhase(ctx, report)
	if err := ctx.Err(); err != nil {
		return report, err
	}
	a.applyPhase(ctx, report)

	a.reportPhase(ctx, report)

	report.FinishedAt = time.Now()
	a.log.Info("cycle complete",
		"duration", report.Duration().Round(time.Second),
		"scraped", report.Scraped, "new", report.NewJobs,
		"matched", report.Matched, "applied", report.Applied, "failed", report.Failed)
	return report, nil
}

// scrapePhase runs all scrapers in parallel (bounded) and persists new jobs.
func (a *Agent) scrapePhase(ctx context.Context, report *models.CycleReport) {
	results := make([]*models.ScrapeResult, len(a.deps.Scrapers))

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(a.cfg.Agent.MaxConcurrent)
	for i, sc := range a.deps.Scrapers {
		i, sc := i, sc
		g.Go(func() error {
			res, err := sc.Scrape(gctx)
			if res == nil {
				res = &models.ScrapeResult{Source: sc.Name()}
			}
			if err != nil {
				a.log.Error("scraper failed", "source", sc.Name(), "err", err)
				res.Err = err
			}
			results[i] = res
			return nil // never abort siblings on a single scraper failure
		})
	}
	_ = g.Wait()

	for _, res := range results {
		if res == nil {
			continue
		}
		if res.Err != nil {
			report.ScraperError[res.Source] = res.Err.Error()
		}
		report.Scraped += len(res.Jobs)
		for _, job := range res.Jobs {
			seen, err := a.deps.Store.HasSeen(ctx, job.ExternalID, job.Source)
			if err != nil {
				a.log.Error("has seen failed", "job", job.ID, "err", err)
				continue
			}
			if seen {
				continue
			}
			isNew, err := a.deps.Store.SaveJob(ctx, job)
			if err != nil {
				a.log.Error("save job failed", "job", job.ID, "err", err)
				continue
			}
			if isNew {
				report.NewJobs++
			}
		}
	}
	a.log.Info("scrape phase done", "scraped", report.Scraped, "new", report.NewJobs)
}

// matchPhase scores every job in "new" status (sequential to respect the Claude
// rate limit) and routes it to matched/rejected.
func (a *Agent) matchPhase(ctx context.Context, report *models.CycleReport) {
	jobs, err := a.deps.Store.ListByStatus(ctx, models.StatusNew)
	if err != nil {
		a.log.Error("list new jobs failed", "err", err)
		return
	}

	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return
		}
		res, err := a.deps.Matcher.Match(ctx, job, a.cfg.Profile)
		if err != nil {
			// Leave as "new" so it retries next cycle.
			a.log.Error("match failed", "job", job.ID, "err", err)
			continue
		}
		if err := a.deps.Store.UpdateMatch(ctx, job.ID, res.Score, res.Reasoning, res.CoverLetter); err != nil {
			a.log.Error("update match failed", "job", job.ID, "err", err)
			continue
		}

		status := models.StatusRejected
		if res.Score >= a.cfg.Matching.MinScore {
			status = models.StatusMatched
			report.Matched++
		} else {
			report.Rejected++
		}
		if err := a.deps.Store.UpdateJobStatus(ctx, job.ID, status); err != nil {
			a.log.Error("update status failed", "job", job.ID, "err", err)
		}

		a.log.Info("matched", "job", job.ID, "score", res.Score, "status", status)
		if res.Score >= highScoreAlert {
			job.MatchScore = res.Score
			msg := highScoreMessage(job)
			if err := a.deps.Notifier.SendAlert(ctx, msg); err != nil {
				a.log.Warn("alert failed", "err", err)
			}
		}
	}
}

// applyPhase applies to matched jobs scoring at/above the auto-apply threshold.
func (a *Agent) applyPhase(ctx context.Context, report *models.CycleReport) {
	jobs, err := a.deps.Store.ListByStatus(ctx, models.StatusMatched)
	if err != nil {
		a.log.Error("list matched jobs failed", "err", err)
		return
	}

	var profileData map[string]string
	loadProfile := func() map[string]string {
		if profileData != nil {
			return profileData
		}
		p, err := a.deps.Store.GetProfile(ctx)
		if err != nil {
			a.log.Error("get profile failed", "err", err)
			p = map[string]string{}
		}
		profileData = p
		return p
	}

	for _, job := range jobs {
		if err := ctx.Err(); err != nil {
			return
		}
		if job.MatchScore < a.cfg.Matching.AutoApplyAbove {
			continue
		}

		// Classify the platform to decide automate vs notify-only vs unknown.
		platform, err := a.deps.Store.DetectPlatform(ctx, job.ApplyURL)
		if err != nil {
			a.log.Error("detect platform failed", "job", job.ID, "err", err)
			continue
		}
		switch applier.Classify(platform) {
		case applier.ClassNotifyOnly:
			a.notifyManual(ctx, job, "manual application required ("+platform.Name+")")
			a.setSkipped(ctx, job, report)
			continue
		case applier.ClassUnknown:
			a.notifyManual(ctx, job, "unknown ATS — apply manually")
			a.setSkipped(ctx, job, report)
			continue
		}

		if a.cfg.Agent.DryRun || a.deps.Applier == nil {
			a.log.Info("dry run: would apply", "job", job.ID, "platform", platform.Name)
			a.setSkipped(ctx, job, report)
			continue
		}

		_ = a.deps.Store.UpdateJobStatus(ctx, job.ID, models.StatusApplying)
		result, err := a.deps.Applier.Apply(ctx, job, loadProfile(), job.CoverLetter)
		if err != nil {
			a.log.Error("apply errored", "job", job.ID, "err", err)
			a.failJob(ctx, job, err.Error(), "", report)
			continue
		}
		if !result.Success {
			a.failJob(ctx, job, result.FailureReason, result.ScreenshotPath, report)
			continue
		}

		if err := a.deps.Store.UpdateJobStatus(ctx, job.ID, models.StatusApplied); err != nil {
			a.log.Error("update applied status failed", "job", job.ID, "err", err)
		}
		report.Applied++
		a.log.Info("applied", "job", job.ID, "screenshot", result.ScreenshotPath)
	}
}

// reportPhase assembles top matches and sends the cycle report.
func (a *Agent) reportPhase(ctx context.Context, report *models.CycleReport) {
	if matched, err := a.deps.Store.ListByStatus(ctx, models.StatusMatched); err == nil {
		sort.Slice(matched, func(i, j int) bool { return matched[i].MatchScore > matched[j].MatchScore })
		if len(matched) > 5 {
			matched = matched[:5]
		}
		report.TopMatches = matched
	}
	if err := a.deps.Notifier.SendReport(ctx, report); err != nil {
		a.log.Warn("send report failed", "err", err)
	}
}

func (a *Agent) setSkipped(ctx context.Context, job *models.Job, report *models.CycleReport) {
	if err := a.deps.Store.UpdateJobStatus(ctx, job.ID, models.StatusSkipped); err != nil {
		a.log.Error("update skipped status failed", "job", job.ID, "err", err)
	}
	report.Skipped++
}

func (a *Agent) failJob(ctx context.Context, job *models.Job, reason, screenshot string, report *models.CycleReport) {
	if err := a.deps.Store.UpdateJobStatus(ctx, job.ID, models.StatusFailed); err != nil {
		a.log.Error("update failed status failed", "job", job.ID, "err", err)
	}
	job.ScreenshotPath = screenshot
	report.Failed++
	a.log.Error("application failed", "job", job.ID, "reason", reason)
}

func (a *Agent) notifyManual(ctx context.Context, job *models.Job, why string) {
	msg := why + "\n" + job.Title + " @ " + job.Company + "\n" + job.ApplyURL
	if err := a.deps.Notifier.SendAlert(ctx, msg); err != nil {
		a.log.Warn("manual notify failed", "err", err)
	}
}

func highScoreMessage(job *models.Job) string {
	return "🔥 High match!\n" + job.Title + " @ " + job.Company + "\n" + job.ApplyURL
}
