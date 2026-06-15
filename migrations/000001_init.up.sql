-- 000001_init.up.sql — full schema for the job agent.

-- 1. USER PROFILE (personal data, stored once, used by all platforms)
CREATE TABLE profile (
    id          SERIAL PRIMARY KEY,
    field_key   TEXT UNIQUE NOT NULL,           -- canonical name: "email", "first_name", "cv_path"
    field_value TEXT NOT NULL,
    field_type  TEXT NOT NULL DEFAULT 'text',   -- text, file, select, textarea
    updated_at  TIMESTAMPTZ DEFAULT NOW()
);

-- 2. ATS PLATFORMS (Greenhouse, Lever, Workday, etc.)
CREATE TABLE platforms (
    id                     SERIAL PRIMARY KEY,
    name                   TEXT UNIQUE NOT NULL,
    display_name           TEXT NOT NULL,
    url_pattern            TEXT NOT NULL,                  -- regex for detection
    flow_type              TEXT NOT NULL DEFAULT 'single_page', -- single_page, multi_step, email
    pre_actions            JSONB DEFAULT '[]',             -- clicks before form appears
    success_indicator      TEXT,                           -- CSS selector or text proving success
    needs_login            BOOLEAN DEFAULT FALSE,
    wait_between_fields_ms  INTEGER DEFAULT 100,
    enabled                BOOLEAN DEFAULT TRUE,
    created_at             TIMESTAMPTZ DEFAULT NOW()
);

-- 3. FIELD MAPPINGS (maps profile fields -> form CSS selectors per platform)
CREATE TABLE field_mappings (
    id                 SERIAL PRIMARY KEY,
    platform_id        INTEGER NOT NULL REFERENCES platforms(id),
    profile_field      TEXT NOT NULL,                       -- references profile.field_key
    css_selector       TEXT NOT NULL,
    input_method       TEXT NOT NULL DEFAULT 'type',        -- type, select, click, upload, textarea, custom
    select_value       TEXT,
    custom_script      TEXT,
    is_required        BOOLEAN DEFAULT TRUE,
    step_number        INTEGER DEFAULT 1,
    field_order        INTEGER DEFAULT 0,
    fallback_selectors TEXT[],
    label_text         TEXT,
    created_at         TIMESTAMPTZ DEFAULT NOW(),
    UNIQUE(platform_id, profile_field, step_number)
);

-- 4. MULTI-STEP NAVIGATION (for Workday-style forms)
CREATE TABLE platform_steps (
    id                SERIAL PRIMARY KEY,
    platform_id       INTEGER NOT NULL REFERENCES platforms(id),
    step_number       INTEGER NOT NULL,
    step_name         TEXT,
    next_selector     TEXT,
    arrival_indicator TEXT,
    wait_ms           INTEGER DEFAULT 2000,
    UNIQUE(platform_id, step_number)
);

-- 5. COMMON QUESTIONS (regex -> default answer)
CREATE TABLE common_questions (
    id                SERIAL PRIMARY KEY,
    question_pattern  TEXT NOT NULL,
    answer            TEXT NOT NULL,
    answer_type       TEXT DEFAULT 'text',
    platform_id       INTEGER REFERENCES platforms(id),     -- NULL = all platforms
    created_at        TIMESTAMPTZ DEFAULT NOW()
);

-- 6. JOBS (main job tracking table)
CREATE TABLE jobs (
    id              TEXT PRIMARY KEY,
    external_id     TEXT NOT NULL,
    source          TEXT NOT NULL,
    title           TEXT NOT NULL,
    company         TEXT NOT NULL,
    location        TEXT DEFAULT '',
    description     TEXT DEFAULT '',
    requirements    TEXT DEFAULT '',
    url             TEXT DEFAULT '',
    apply_url       TEXT DEFAULT '',
    salary          TEXT DEFAULT '',
    job_type        TEXT DEFAULT '',
    remote          BOOLEAN DEFAULT FALSE,
    posted_at       TIMESTAMPTZ,
    discovered_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    status          TEXT NOT NULL DEFAULT 'new',
    match_score     REAL DEFAULT 0,
    match_reason    TEXT DEFAULT '',
    cover_letter    TEXT DEFAULT '',
    applied_at      TIMESTAMPTZ,
    failure_reason  TEXT DEFAULT '',
    screenshot_path TEXT DEFAULT '',
    platform_id     INTEGER REFERENCES platforms(id),
    UNIQUE(external_id, source)
);
CREATE INDEX idx_jobs_status ON jobs(status);
CREATE INDEX idx_jobs_company ON jobs(company);

-- 7. APPLICATION LOG (audit trail for debugging)
CREATE TABLE application_log (
    id              SERIAL PRIMARY KEY,
    job_id          TEXT NOT NULL REFERENCES jobs(id),
    platform_id     INTEGER REFERENCES platforms(id),
    action          TEXT NOT NULL,                          -- navigate, fill_field, upload, click, submit, error
    detail          TEXT,
    selector        TEXT,
    success         BOOLEAN DEFAULT TRUE,
    error_message   TEXT,
    screenshot_path TEXT,
    step_number     INTEGER,
    timestamp       TIMESTAMPTZ DEFAULT NOW()
);
CREATE INDEX idx_application_log_job ON application_log(job_id);

-- 8. SELECTOR HEALTH (track broken selectors)
CREATE TABLE selector_health (
    id               SERIAL PRIMARY KEY,
    platform_id      INTEGER NOT NULL REFERENCES platforms(id),
    field_mapping_id INTEGER NOT NULL REFERENCES field_mappings(id),
    last_success     TIMESTAMPTZ,
    last_failure     TIMESTAMPTZ,
    failure_count    INTEGER DEFAULT 0,
    success_count    INTEGER DEFAULT 0,
    is_broken        BOOLEAN DEFAULT FALSE,
    UNIQUE(field_mapping_id)
);
