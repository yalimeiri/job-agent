-- 000002_seed.up.sql — seed ATS platforms, field mappings and common questions.
-- New platforms are added by INSERT, not by code changes.

-- ---------------------------------------------------------------------------
-- Platforms
-- ---------------------------------------------------------------------------
INSERT INTO platforms (name, display_name, url_pattern, flow_type, pre_actions, success_indicator, needs_login, wait_between_fields_ms, enabled) VALUES
  ('greenhouse', 'Greenhouse', 'boards\.greenhouse\.io|job-boards\.greenhouse\.io|greenhouse\.io/embed', 'single_page',
     '[]'::jsonb,
     '#application_confirmation, .application--confirmation', FALSE, 120, TRUE),

  ('lever', 'Lever', 'jobs\.lever\.co', 'single_page',
     '[{"type":"click","selector":".postings-btn, a.template-btn-submit","wait_ms":1500}]'::jsonb,
     '.application-confirmation, .confirmation-page', FALSE, 120, TRUE),

  ('workday', 'Workday', 'myworkdayjobs\.com|wd\d+\.myworkdayjobs\.com', 'multi_step',
     '[{"type":"click","selector":"a[data-automation-id=\"adventureButton\"]","wait_ms":2500}]'::jsonb,
     '[data-automation-id="confirmationPage"]', TRUE, 200, TRUE),

  ('ashby', 'Ashby', 'jobs\.ashbyhq\.com', 'single_page',
     '[{"type":"click","selector":"button._applyButton, a[href*=\"/application\"]","wait_ms":1500}]'::jsonb,
     '[class*="ApplicationFormSubmittedMessage"]', FALSE, 120, TRUE),

  ('comeet', 'Comeet', 'comeet\.com|comeet\.co', 'single_page',
     '[{"type":"click","selector":".comeet-apply-button","wait_ms":1500}]'::jsonb,
     '.comeet-thank-you, .thank-you', FALSE, 150, TRUE),

  ('smartrecruiters', 'SmartRecruiters', 'jobs\.smartrecruiters\.com|careers\.smartrecruiters\.com', 'single_page',
     '[{"type":"click","selector":"button#js-application-apply, a.js-apply","wait_ms":1500}]'::jsonb,
     '.thank-you, [data-test="application-confirmation"]', FALSE, 150, TRUE),

  -- LinkedIn: notify only, never automated.
  ('linkedin', 'LinkedIn', 'linkedin\.com/jobs', 'single_page',
     '[]'::jsonb, NULL, TRUE, 100, FALSE);

-- ---------------------------------------------------------------------------
-- Placeholder profile rows. Real values are upserted from config on startup
-- (the agent syncs config.profile -> this table). cv_path / cover_letter use
-- specific field_types so the applier knows how to handle them.
-- ---------------------------------------------------------------------------
INSERT INTO profile (field_key, field_value, field_type) VALUES
  ('first_name',   'Your',                     'text'),
  ('last_name',    'Name',                     'text'),
  ('full_name',    'Your Name',                'text'),
  ('email',        'you@example.com',          'text'),
  ('phone',        '+972-50-000-0000',         'text'),
  ('linkedin_url', 'https://linkedin.com/in/you', 'text'),
  ('cv_path',      './assets/cv.pdf',          'file'),
  ('current_company', '',                      'text'),
  ('location',     'Tel Aviv, Israel',         'text')
ON CONFLICT (field_key) DO NOTHING;

-- ---------------------------------------------------------------------------
-- Field mappings: Greenhouse
-- ---------------------------------------------------------------------------
WITH gh AS (SELECT id FROM platforms WHERE name = 'greenhouse')
INSERT INTO field_mappings
  (platform_id, profile_field, css_selector, input_method, is_required, step_number, field_order, fallback_selectors, label_text) VALUES
  ((SELECT id FROM gh), 'first_name',   'input#first_name', 'type',   TRUE,  1, 1, ARRAY['input[name="job_application[first_name]"]','input[autocomplete="given-name"]'], 'First Name'),
  ((SELECT id FROM gh), 'last_name',    'input#last_name',  'type',   TRUE,  1, 2, ARRAY['input[name="job_application[last_name]"]','input[autocomplete="family-name"]'], 'Last Name'),
  ((SELECT id FROM gh), 'email',        'input#email',      'type',   TRUE,  1, 3, ARRAY['input[name="job_application[email]"]','input[type="email"]'], 'Email'),
  ((SELECT id FROM gh), 'phone',        'input#phone',      'type',   FALSE, 1, 4, ARRAY['input[name="job_application[phone]"]','input[type="tel"]'], 'Phone'),
  ((SELECT id FROM gh), 'cv_path',      'input#resume',     'upload', TRUE,  1, 5, ARRAY['input[type="file"][name*="resume"]','input[type="file"]'], 'Resume'),
  ((SELECT id FROM gh), 'cover_letter', 'textarea#cover_letter_text', 'textarea', FALSE, 1, 6, ARRAY['textarea[name*="cover_letter"]'], 'Cover Letter');

-- ---------------------------------------------------------------------------
-- Field mappings: Lever
-- ---------------------------------------------------------------------------
WITH lv AS (SELECT id FROM platforms WHERE name = 'lever')
INSERT INTO field_mappings
  (platform_id, profile_field, css_selector, input_method, is_required, step_number, field_order, fallback_selectors, label_text) VALUES
  ((SELECT id FROM lv), 'full_name',    'input[name="name"]',           'type',   TRUE,  1, 1, ARRAY['input#name'], 'Full name'),
  ((SELECT id FROM lv), 'email',        'input[name="email"]',          'type',   TRUE,  1, 2, ARRAY['input[type="email"]'], 'Email'),
  ((SELECT id FROM lv), 'phone',        'input[name="phone"]',          'type',   FALSE, 1, 3, ARRAY['input[type="tel"]'], 'Phone'),
  ((SELECT id FROM lv), 'current_company','input[name="org"]',          'type',   FALSE, 1, 4, ARRAY['input#org'], 'Current company'),
  ((SELECT id FROM lv), 'linkedin_url', 'input[name="urls[LinkedIn]"]', 'type',   FALSE, 1, 5, ARRAY['input[name*="LinkedIn"]'], 'LinkedIn URL'),
  ((SELECT id FROM lv), 'cv_path',      'input[name="resume"]',         'upload', TRUE,  1, 6, ARRAY['input[type="file"]'], 'Resume');

-- ---------------------------------------------------------------------------
-- Common application questions (regex pattern -> default answer)
-- ---------------------------------------------------------------------------
INSERT INTO common_questions (question_pattern, answer, answer_type, platform_id) VALUES
  ('(?i)authoriz(ed|ation) to work',                       'Yes',          'text', NULL),
  ('(?i)require (visa )?sponsorship|need sponsorship',     'No',           'text', NULL),
  ('(?i)legally (allowed|eligible) to work',               'Yes',          'text', NULL),
  ('(?i)willing to relocate',                              'Yes',          'text', NULL),
  ('(?i)(notice period|when can you start|availability)',  'Immediately',  'text', NULL),
  ('(?i)years? of experience',                             '5',            'text', NULL),
  ('(?i)(expected|desired) salary',                        'Negotiable',   'text', NULL),
  ('(?i)how did you hear about',                           'Company website', 'text', NULL),
  ('(?i)are you (over|at least) 18',                       'Yes',          'text', NULL),
  ('(?i)gender|race|ethnicity|disability|veteran',         'Prefer not to say', 'select', NULL);
