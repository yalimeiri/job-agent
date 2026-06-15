// Package models holds the domain types shared across the agent: jobs,
// matching results, ATS platform definitions, field mappings and the various
// report/log structures. Keeping every type in one package avoids import
// cycles between store, scraper, matcher and applier.
package models

import "time"

// JobStatus is the lifecycle state of a discovered job.
type JobStatus string

const (
	StatusNew      JobStatus = "new"      // freshly scraped, not yet evaluated
	StatusMatched  JobStatus = "matched"  // scored at or above the minimum threshold
	StatusRejected JobStatus = "rejected" // scored below the minimum threshold
	StatusApplying JobStatus = "applying" // application in progress
	StatusApplied  JobStatus = "applied"  // application submitted successfully
	StatusFailed   JobStatus = "failed"   // application attempt failed
	StatusSkipped  JobStatus = "skipped"  // intentionally not applied (e.g. LinkedIn / unknown ATS)
)

// Valid reports whether s is a recognised status.
func (s JobStatus) Valid() bool {
	switch s {
	case StatusNew, StatusMatched, StatusRejected, StatusApplying,
		StatusApplied, StatusFailed, StatusSkipped:
		return true
	default:
		return false
	}
}

// Job is a single job listing as discovered, tracked and (optionally) applied to.
type Job struct {
	ID             string     `json:"id"` // deterministic: source + ":" + external_id
	ExternalID     string     `json:"external_id"`
	Source         string     `json:"source"`
	Title          string     `json:"title"`
	Company        string     `json:"company"`
	Location       string     `json:"location"`
	Description    string     `json:"description"`
	Requirements   string     `json:"requirements"`
	URL            string     `json:"url"`
	ApplyURL       string     `json:"apply_url"`
	Salary         string     `json:"salary"`
	JobType        string     `json:"job_type"`
	Remote         bool       `json:"remote"`
	PostedAt       *time.Time `json:"posted_at,omitempty"`
	DiscoveredAt   time.Time  `json:"discovered_at"`
	Status         JobStatus  `json:"status"`
	MatchScore     float64    `json:"match_score"`
	MatchReason    string     `json:"match_reason"`
	CoverLetter    string     `json:"cover_letter"`
	AppliedAt      *time.Time `json:"applied_at,omitempty"`
	FailureReason  string     `json:"failure_reason"`
	ScreenshotPath string     `json:"screenshot_path"`
	PlatformID     *int       `json:"platform_id,omitempty"`
}

// ScrapeResult is what a Scraper returns for a single run.
type ScrapeResult struct {
	Source string `json:"source"`
	Jobs   []*Job `json:"jobs"`
	// Err holds a non-fatal error for this source so the agent can report it
	// without aborting the whole scrape phase.
	Err error `json:"-"`
}

// MatchResult is the structured verdict produced by a Matcher.
type MatchResult struct {
	Score        float64  `json:"score"`    // 0.0 - 1.0
	Relevant     bool     `json:"relevant"` // convenience flag mirroring score >= threshold
	Reasoning    string   `json:"reasoning"`
	CoverLetter  string   `json:"cover_letter"`
	KeySkillGaps []string `json:"key_skill_gaps"`
	Strengths    []string `json:"strengths"`
}

// ApplicationResult reports the outcome of an Applier.Apply call.
type ApplicationResult struct {
	Success        bool   `json:"success"`
	FailureReason  string `json:"failure_reason"`
	ScreenshotPath string `json:"screenshot_path"`
	StepsCompleted int    `json:"steps_completed"`
}

// Platform is an ATS definition (Greenhouse, Lever, Workday, ...).
type Platform struct {
	ID                  int       `json:"id"`
	Name                string    `json:"name"`
	DisplayName         string    `json:"display_name"`
	URLPattern          string    `json:"url_pattern"`
	FlowType            string    `json:"flow_type"` // single_page, multi_step, email
	PreActions          []Action  `json:"pre_actions"`
	SuccessIndicator    string    `json:"success_indicator"`
	NeedsLogin          bool      `json:"needs_login"`
	WaitBetweenFieldsMs int       `json:"wait_between_fields_ms"`
	Enabled             bool      `json:"enabled"`
	CreatedAt           time.Time `json:"created_at"`
}

// Action is a pre-form interaction (stored as JSONB in platforms.pre_actions).
type Action struct {
	Type     string `json:"type"`     // click, wait, scroll, eval
	Selector string `json:"selector"` // CSS selector for click/scroll
	Value    string `json:"value"`    // JS for eval, ms for wait
	WaitMs   int    `json:"wait_ms"`
}

// FieldMapping maps a single profile field to a form selector for a platform.
type FieldMapping struct {
	ID                int      `json:"id"`
	PlatformID        int      `json:"platform_id"`
	ProfileField      string   `json:"profile_field"`
	CSSSelector       string   `json:"css_selector"`
	InputMethod       string   `json:"input_method"` // type, select, click, upload, textarea, custom
	SelectValue       string   `json:"select_value"`
	CustomScript      string   `json:"custom_script"`
	IsRequired        bool     `json:"is_required"`
	StepNumber        int      `json:"step_number"`
	FieldOrder        int      `json:"field_order"`
	FallbackSelectors []string `json:"fallback_selectors"`
	LabelText         string   `json:"label_text"`
}

// PlatformStep describes navigation for a multi-step (Workday-style) flow.
type PlatformStep struct {
	ID               int    `json:"id"`
	PlatformID       int    `json:"platform_id"`
	StepNumber       int    `json:"step_number"`
	StepName         string `json:"step_name"`
	NextSelector     string `json:"next_selector"`
	ArrivalIndicator string `json:"arrival_indicator"`
	WaitMs           int    `json:"wait_ms"`
}

// CommonQuestion maps a question pattern to a default answer.
type CommonQuestion struct {
	ID              int    `json:"id"`
	QuestionPattern string `json:"question_pattern"`
	Answer          string `json:"answer"`
	AnswerType      string `json:"answer_type"`
	PlatformID      *int   `json:"platform_id,omitempty"` // nil = applies to all platforms
}

// ApplicationLogEntry is one row in the application_log audit trail.
type ApplicationLogEntry struct {
	JobID          string    `json:"job_id"`
	PlatformID     *int      `json:"platform_id,omitempty"`
	Action         string    `json:"action"` // navigate, fill_field, upload, click, submit, error
	Detail         string    `json:"detail"`
	Selector       string    `json:"selector"`
	Success        bool      `json:"success"`
	ErrorMessage   string    `json:"error_message"`
	ScreenshotPath string    `json:"screenshot_path"`
	StepNumber     int       `json:"step_number"`
	Timestamp      time.Time `json:"timestamp"`
}

// CycleReport summarises a single agent cycle for the notifier.
type CycleReport struct {
	StartedAt    time.Time         `json:"started_at"`
	FinishedAt   time.Time         `json:"finished_at"`
	Scraped      int               `json:"scraped"`
	NewJobs      int               `json:"new_jobs"`
	Matched      int               `json:"matched"`
	Rejected     int               `json:"rejected"`
	Applied      int               `json:"applied"`
	Failed       int               `json:"failed"`
	Skipped      int               `json:"skipped"`
	TopMatches   []*Job            `json:"top_matches"`
	ScraperError map[string]string `json:"scraper_errors"` // source -> error
	DryRun       bool              `json:"dry_run"`
}

// Duration returns the wall-clock length of the cycle.
func (r *CycleReport) Duration() time.Duration {
	return r.FinishedAt.Sub(r.StartedAt)
}
