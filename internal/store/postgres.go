package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/yalimeiri/job-agent/internal/models"
)

// Postgres is the pgx/v5 implementation of Store.
type Postgres struct {
	pool *pgxpool.Pool

	// reCache memoises compiled url_pattern regexes for platform detection.
	reMu    sync.Mutex
	reCache map[string]*regexp.Regexp
}

// New opens a connection pool to dsn and verifies connectivity. It retries the
// initial ping with backoff so a container started alongside Postgres can wait
// for the DB to become ready.
func New(ctx context.Context, dsn string) (*Postgres, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	cfg.MaxConns = 8

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	var pingErr error
	for attempt := 0; attempt < 10; attempt++ {
		if pingErr = pool.Ping(ctx); pingErr == nil {
			break
		}
		select {
		case <-ctx.Done():
			pool.Close()
			return nil, ctx.Err()
		case <-time.After(time.Duration(attempt+1) * 500 * time.Millisecond):
		}
	}
	if pingErr != nil {
		pool.Close()
		return nil, fmt.Errorf("ping db: %w", pingErr)
	}

	return &Postgres{pool: pool, reCache: map[string]*regexp.Regexp{}}, nil
}

// Close releases the connection pool.
func (p *Postgres) Close() error {
	p.pool.Close()
	return nil
}

// ---------------------------------------------------------------------------
// Jobs
// ---------------------------------------------------------------------------

// SaveJob inserts a job, ignoring duplicates by (external_id, source). It
// reports whether the row was newly created.
func (p *Postgres) SaveJob(ctx context.Context, job *models.Job) (bool, error) {
	if job.Status == "" {
		job.Status = models.StatusNew
	}
	if job.DiscoveredAt.IsZero() {
		job.DiscoveredAt = time.Now()
	}
	const q = `
INSERT INTO jobs (id, external_id, source, title, company, location, description,
                  requirements, url, apply_url, salary, job_type, remote,
                  posted_at, discovered_at, status)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
ON CONFLICT (external_id, source) DO NOTHING`

	tag, err := p.pool.Exec(ctx, q,
		job.ID, job.ExternalID, job.Source, job.Title, job.Company, job.Location,
		job.Description, job.Requirements, job.URL, job.ApplyURL, job.Salary,
		job.JobType, job.Remote, job.PostedAt, job.DiscoveredAt, job.Status,
	)
	if err != nil {
		return false, fmt.Errorf("save job %s: %w", job.ID, err)
	}
	return tag.RowsAffected() == 1, nil
}

// UpdateJobStatus sets the status (and applied_at when transitioning to applied).
func (p *Postgres) UpdateJobStatus(ctx context.Context, id string, status models.JobStatus) error {
	const q = `
UPDATE jobs
SET status = $2,
    applied_at = CASE WHEN $2 = 'applied' THEN NOW() ELSE applied_at END
WHERE id = $1`
	_, err := p.pool.Exec(ctx, q, id, status)
	if err != nil {
		return fmt.Errorf("update status %s: %w", id, err)
	}
	return nil
}

// UpdateMatch records the match score, reasoning and generated cover letter.
func (p *Postgres) UpdateMatch(ctx context.Context, id string, score float64, reason, coverLetter string) error {
	const q = `UPDATE jobs SET match_score = $2, match_reason = $3, cover_letter = $4 WHERE id = $1`
	_, err := p.pool.Exec(ctx, q, id, score, reason, coverLetter)
	if err != nil {
		return fmt.Errorf("update match %s: %w", id, err)
	}
	return nil
}

// ListByStatus returns all jobs in the given status, newest first.
func (p *Postgres) ListByStatus(ctx context.Context, status models.JobStatus) ([]*models.Job, error) {
	const q = `
SELECT id, external_id, source, title, company, location, description, requirements,
       url, apply_url, salary, job_type, remote, posted_at, discovered_at, status,
       match_score, match_reason, cover_letter, applied_at, failure_reason,
       screenshot_path, platform_id
FROM jobs WHERE status = $1 ORDER BY match_score DESC, discovered_at DESC`

	rows, err := p.pool.Query(ctx, q, status)
	if err != nil {
		return nil, fmt.Errorf("list by status %s: %w", status, err)
	}
	defer rows.Close()

	var jobs []*models.Job
	for rows.Next() {
		j := &models.Job{}
		if err := rows.Scan(
			&j.ID, &j.ExternalID, &j.Source, &j.Title, &j.Company, &j.Location,
			&j.Description, &j.Requirements, &j.URL, &j.ApplyURL, &j.Salary,
			&j.JobType, &j.Remote, &j.PostedAt, &j.DiscoveredAt, &j.Status,
			&j.MatchScore, &j.MatchReason, &j.CoverLetter, &j.AppliedAt,
			&j.FailureReason, &j.ScreenshotPath, &j.PlatformID,
		); err != nil {
			return nil, fmt.Errorf("scan job: %w", err)
		}
		jobs = append(jobs, j)
	}
	return jobs, rows.Err()
}

// HasSeen reports whether a job with this external id / source already exists.
func (p *Postgres) HasSeen(ctx context.Context, externalID, source string) (bool, error) {
	const q = `SELECT EXISTS(SELECT 1 FROM jobs WHERE external_id = $1 AND source = $2)`
	var exists bool
	if err := p.pool.QueryRow(ctx, q, externalID, source).Scan(&exists); err != nil {
		return false, fmt.Errorf("has seen: %w", err)
	}
	return exists, nil
}

// ---------------------------------------------------------------------------
// Platform detection
// ---------------------------------------------------------------------------

// DetectPlatform finds the enabled platform whose url_pattern regex matches the
// apply URL. Returns (nil, nil) when no platform matches.
func (p *Postgres) DetectPlatform(ctx context.Context, applyURL string) (*models.Platform, error) {
	const q = `
SELECT id, name, display_name, url_pattern, flow_type, pre_actions, success_indicator,
       needs_login, wait_between_fields_ms, enabled, created_at
FROM platforms ORDER BY id`

	rows, err := p.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("load platforms: %w", err)
	}
	defer rows.Close()

	var platforms []*models.Platform
	for rows.Next() {
		pl := &models.Platform{}
		var preActions []byte
		var success *string
		if err := rows.Scan(
			&pl.ID, &pl.Name, &pl.DisplayName, &pl.URLPattern, &pl.FlowType,
			&preActions, &success, &pl.NeedsLogin, &pl.WaitBetweenFieldsMs,
			&pl.Enabled, &pl.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan platform: %w", err)
		}
		if success != nil {
			pl.SuccessIndicator = *success
		}
		if len(preActions) > 0 {
			if err := json.Unmarshal(preActions, &pl.PreActions); err != nil {
				return nil, fmt.Errorf("unmarshal pre_actions for %s: %w", pl.Name, err)
			}
		}
		platforms = append(platforms, pl)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, pl := range platforms {
		re, err := p.compile(pl.URLPattern)
		if err != nil {
			return nil, fmt.Errorf("bad url_pattern for %s: %w", pl.Name, err)
		}
		if re.MatchString(applyURL) {
			return pl, nil
		}
	}
	return nil, nil
}

func (p *Postgres) compile(pattern string) (*regexp.Regexp, error) {
	p.reMu.Lock()
	defer p.reMu.Unlock()
	if re, ok := p.reCache[pattern]; ok {
		return re, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	p.reCache[pattern] = re
	return re, nil
}

// SetJobPlatform records which platform a job was detected as.
func (p *Postgres) SetJobPlatform(ctx context.Context, jobID string, platformID int) error {
	const q = `UPDATE jobs SET platform_id = $2 WHERE id = $1`
	_, err := p.pool.Exec(ctx, q, jobID, platformID)
	if err != nil {
		return fmt.Errorf("set job platform: %w", err)
	}
	return nil
}

// GetFieldMappings returns the field mappings for a platform/step ordered for
// deterministic filling.
func (p *Postgres) GetFieldMappings(ctx context.Context, platformID, stepNumber int) ([]models.FieldMapping, error) {
	const q = `
SELECT id, platform_id, profile_field, css_selector, input_method,
       COALESCE(select_value,''), COALESCE(custom_script,''), is_required,
       step_number, field_order, COALESCE(fallback_selectors, '{}'), COALESCE(label_text,'')
FROM field_mappings
WHERE platform_id = $1 AND step_number = $2
ORDER BY field_order, id`

	rows, err := p.pool.Query(ctx, q, platformID, stepNumber)
	if err != nil {
		return nil, fmt.Errorf("get field mappings: %w", err)
	}
	defer rows.Close()

	var out []models.FieldMapping
	for rows.Next() {
		var m models.FieldMapping
		if err := rows.Scan(
			&m.ID, &m.PlatformID, &m.ProfileField, &m.CSSSelector, &m.InputMethod,
			&m.SelectValue, &m.CustomScript, &m.IsRequired, &m.StepNumber,
			&m.FieldOrder, &m.FallbackSelectors, &m.LabelText,
		); err != nil {
			return nil, fmt.Errorf("scan field mapping: %w", err)
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// GetPlatformSteps returns the multi-step navigation rows for a platform.
func (p *Postgres) GetPlatformSteps(ctx context.Context, platformID int) ([]models.PlatformStep, error) {
	const q = `
SELECT id, platform_id, step_number, COALESCE(step_name,''), COALESCE(next_selector,''),
       COALESCE(arrival_indicator,''), wait_ms
FROM platform_steps WHERE platform_id = $1 ORDER BY step_number`

	rows, err := p.pool.Query(ctx, q, platformID)
	if err != nil {
		return nil, fmt.Errorf("get platform steps: %w", err)
	}
	defer rows.Close()

	var out []models.PlatformStep
	for rows.Next() {
		var s models.PlatformStep
		if err := rows.Scan(&s.ID, &s.PlatformID, &s.StepNumber, &s.StepName,
			&s.NextSelector, &s.ArrivalIndicator, &s.WaitMs); err != nil {
			return nil, fmt.Errorf("scan platform step: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetCommonQuestions returns questions that apply globally (platform_id IS NULL)
// plus those scoped to the given platform.
func (p *Postgres) GetCommonQuestions(ctx context.Context, platformID *int) ([]models.CommonQuestion, error) {
	const q = `
SELECT id, question_pattern, answer, COALESCE(answer_type,'text'), platform_id
FROM common_questions
WHERE platform_id IS NULL OR platform_id = $1
ORDER BY id`

	rows, err := p.pool.Query(ctx, q, platformID)
	if err != nil {
		return nil, fmt.Errorf("get common questions: %w", err)
	}
	defer rows.Close()

	var out []models.CommonQuestion
	for rows.Next() {
		var cq models.CommonQuestion
		if err := rows.Scan(&cq.ID, &cq.QuestionPattern, &cq.Answer, &cq.AnswerType, &cq.PlatformID); err != nil {
			return nil, fmt.Errorf("scan common question: %w", err)
		}
		out = append(out, cq)
	}
	return out, rows.Err()
}

// ---------------------------------------------------------------------------
// Profile
// ---------------------------------------------------------------------------

// GetProfile returns the field_key -> field_value map of the user's profile.
func (p *Postgres) GetProfile(ctx context.Context) (map[string]string, error) {
	rows, err := p.pool.Query(ctx, `SELECT field_key, field_value FROM profile`)
	if err != nil {
		return nil, fmt.Errorf("get profile: %w", err)
	}
	defer rows.Close()

	out := map[string]string{}
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, fmt.Errorf("scan profile: %w", err)
		}
		out[k] = v
	}
	return out, rows.Err()
}

// UpsertProfileField inserts or updates a single profile field.
func (p *Postgres) UpsertProfileField(ctx context.Context, key, value, fieldType string) error {
	if fieldType == "" {
		fieldType = "text"
	}
	const q = `
INSERT INTO profile (field_key, field_value, field_type, updated_at)
VALUES ($1, $2, $3, NOW())
ON CONFLICT (field_key)
DO UPDATE SET field_value = EXCLUDED.field_value,
              field_type = EXCLUDED.field_type,
              updated_at = NOW()`
	_, err := p.pool.Exec(ctx, q, key, value, fieldType)
	if err != nil {
		return fmt.Errorf("upsert profile %s: %w", key, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Logging
// ---------------------------------------------------------------------------

// LogApplicationAction appends a row to the application audit log.
func (p *Postgres) LogApplicationAction(ctx context.Context, e models.ApplicationLogEntry) error {
	const q = `
INSERT INTO application_log
  (job_id, platform_id, action, detail, selector, success, error_message, screenshot_path, step_number)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`
	_, err := p.pool.Exec(ctx, q,
		e.JobID, e.PlatformID, e.Action, e.Detail, e.Selector, e.Success,
		e.ErrorMessage, e.ScreenshotPath, e.StepNumber,
	)
	if err != nil {
		return fmt.Errorf("log application action: %w", err)
	}
	return nil
}

// UpdateSelectorHealth records a success/failure for a field mapping selector
// and flags it broken after repeated failures.
func (p *Postgres) UpdateSelectorHealth(ctx context.Context, mappingID int, success bool) error {
	const q = `
INSERT INTO selector_health
  (platform_id, field_mapping_id, last_success, last_failure, success_count, failure_count, is_broken)
SELECT fm.platform_id, fm.id,
       CASE WHEN $2 THEN NOW() END,
       CASE WHEN $2 THEN NULL ELSE NOW() END,
       CASE WHEN $2 THEN 1 ELSE 0 END,
       CASE WHEN $2 THEN 0 ELSE 1 END,
       FALSE
FROM field_mappings fm WHERE fm.id = $1
ON CONFLICT (field_mapping_id) DO UPDATE SET
  last_success  = CASE WHEN $2 THEN NOW() ELSE selector_health.last_success END,
  last_failure  = CASE WHEN $2 THEN selector_health.last_failure ELSE NOW() END,
  success_count = selector_health.success_count + CASE WHEN $2 THEN 1 ELSE 0 END,
  failure_count = CASE WHEN $2 THEN 0 ELSE selector_health.failure_count + 1 END,
  is_broken     = (CASE WHEN $2 THEN 0 ELSE selector_health.failure_count + 1 END) >= 3`
	_, err := p.pool.Exec(ctx, q, mappingID, success)
	if err != nil {
		return fmt.Errorf("update selector health %d: %w", mappingID, err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Stats
// ---------------------------------------------------------------------------

// GetStats returns a count of jobs grouped by status.
func (p *Postgres) GetStats(ctx context.Context) (map[models.JobStatus]int, error) {
	rows, err := p.pool.Query(ctx, `SELECT status, COUNT(*) FROM jobs GROUP BY status`)
	if err != nil {
		return nil, fmt.Errorf("get stats: %w", err)
	}
	defer rows.Close()

	out := map[models.JobStatus]int{}
	for rows.Next() {
		var s models.JobStatus
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return nil, fmt.Errorf("scan stats: %w", err)
		}
		out[s] = n
	}
	return out, rows.Err()
}

// ErrNotFound is returned when a single-row lookup finds nothing.
var ErrNotFound = errors.New("not found")

// compile-time assertion that pgx.ErrNoRows is handled where relevant.
var _ = pgx.ErrNoRows

// Ensure Postgres satisfies Store.
var _ Store = (*Postgres)(nil)
