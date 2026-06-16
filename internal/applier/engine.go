package applier

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"github.com/yalimeiri/job-agent/internal/models"
	"github.com/yalimeiri/job-agent/internal/store"
)

// Engine is the data-driven, rod-backed Applier. It reads every selector and
// answer from the store so new ATS platforms require no code changes.
type Engine struct {
	store         store.Store
	screenshotDir string
	log           *slog.Logger
	headless      bool

	mu      sync.Mutex
	browser *rod.Browser // lazily launched, reused across applications
}

// Options configures the engine.
type Options struct {
	ScreenshotDir string
	Headless      bool
	Logger        *slog.Logger
}

// NewEngine constructs an Engine. The browser is launched lazily on first Apply.
func NewEngine(st store.Store, opts Options) *Engine {
	if opts.ScreenshotDir == "" {
		opts.ScreenshotDir = "./screenshots"
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &Engine{
		store:         st,
		screenshotDir: opts.ScreenshotDir,
		headless:      opts.Headless,
		log:           opts.Logger.With("component", "applier"),
	}
}

// Apply drives the full application flow for a job. It returns an
// ApplicationResult describing the outcome; an error is returned only for
// unexpected failures (a failed-but-handled application yields Success=false).
func (e *Engine) Apply(ctx context.Context, job *models.Job, profileData map[string]string, coverLetter string) (*models.ApplicationResult, error) {
	platform, err := e.store.DetectPlatform(ctx, job.ApplyURL)
	if err != nil {
		return nil, fmt.Errorf("detect platform: %w", err)
	}
	if platform == nil {
		return &models.ApplicationResult{Success: false, FailureReason: "no platform matched apply URL"}, nil
	}
	if Classify(platform) != ClassAutomate {
		return &models.ApplicationResult{Success: false, FailureReason: "platform is not automatable: " + platform.Name}, nil
	}
	if err := e.store.SetJobPlatform(ctx, job.ID, platform.ID); err != nil {
		e.log.Warn("set job platform failed", "job", job.ID, "err", err)
	}

	// cover_letter is a per-job profile value injected into the data map.
	data := make(map[string]string, len(profileData)+1)
	for k, v := range profileData {
		data[k] = v
	}
	if coverLetter != "" {
		data["cover_letter"] = coverLetter
	}

	log := e.log.With("job", job.ID, "platform", platform.Name)

	page, cleanup, err := e.openPage(ctx, job.ApplyURL)
	if err != nil {
		return nil, fmt.Errorf("open page: %w", err)
	}
	defer cleanup()
	e.logAction(ctx, job, platform, models.ApplicationLogEntry{Action: "navigate", Detail: job.ApplyURL, Success: true})

	// Pre-actions (e.g. click the "Apply" button to reveal the form).
	for _, a := range platform.PreActions {
		if err := e.runAction(page, a); err != nil {
			log.Warn("pre-action failed", "type", a.Type, "selector", a.Selector, "err", err)
		}
	}

	result := &models.ApplicationResult{}

	// Determine the steps to walk. single_page -> one step (1).
	steps, err := e.store.GetPlatformSteps(ctx, platform.ID)
	if err != nil {
		return nil, fmt.Errorf("get platform steps: %w", err)
	}
	stepNumbers := []int{1}
	if platform.FlowType == "multi_step" && len(steps) > 0 {
		stepNumbers = stepNumbers[:0]
		for _, s := range steps {
			stepNumbers = append(stepNumbers, s.StepNumber)
		}
	}

	for idx, stepNum := range stepNumbers {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := e.fillStep(ctx, page, job, platform, stepNum, data); err != nil {
			log.Error("fill step failed", "step", stepNum, "err", err)
			result.FailureReason = err.Error()
			result.ScreenshotPath = e.screenshot(page, job, "error")
			return result, nil
		}
		result.StepsCompleted++

		// Advance to the next step if there is one.
		if idx < len(stepNumbers)-1 {
			if err := e.advance(ctx, page, steps, stepNum); err != nil {
				log.Error("advance step failed", "step", stepNum, "err", err)
				result.FailureReason = "advance: " + err.Error()
				result.ScreenshotPath = e.screenshot(page, job, "error")
				return result, nil
			}
		}
	}

	// Answer free-form common questions on the final form.
	e.answerCommonQuestions(ctx, page, job, platform)

	// Pre-submit proof.
	preShot := e.screenshot(page, job, "pre_submit")

	// Submit and verify.
	if err := e.submit(ctx, page, job, platform); err != nil {
		log.Error("submit failed", "err", err)
		result.FailureReason = err.Error()
		result.ScreenshotPath = e.screenshot(page, job, "error")
		return result, nil
	}

	result.ScreenshotPath = e.screenshot(page, job, "post_submit")
	if result.ScreenshotPath == "" {
		result.ScreenshotPath = preShot
	}
	result.Success = true
	log.Info("application submitted", "screenshot", result.ScreenshotPath)
	return result, nil
}

// fillStep fills every mapping for the given step.
func (e *Engine) fillStep(ctx context.Context, page *rod.Page, job *models.Job, platform *models.Platform, stepNum int, data map[string]string) error {
	mappings, err := e.store.GetFieldMappings(ctx, platform.ID, stepNum)
	if err != nil {
		return fmt.Errorf("get field mappings: %w", err)
	}

	for _, m := range mappings {
		if err := ctx.Err(); err != nil {
			return err
		}
		el, usedSelector, found := e.findElement(page, m)
		if !found {
			e.logAction(ctx, job, platform, models.ApplicationLogEntry{
				Action: "fill_field", Detail: m.ProfileField, Selector: m.CSSSelector,
				Success: false, ErrorMessage: "element not found", StepNumber: stepNum,
			})
			_ = e.store.UpdateSelectorHealth(ctx, m.ID, false)
			if m.IsRequired {
				return fmt.Errorf("required field %q not found (selector %q)", m.ProfileField, m.CSSSelector)
			}
			continue
		}

		if err := e.applyField(page, el, m, data); err != nil {
			e.logAction(ctx, job, platform, models.ApplicationLogEntry{
				Action: "fill_field", Detail: m.ProfileField, Selector: usedSelector,
				Success: false, ErrorMessage: err.Error(), StepNumber: stepNum,
			})
			_ = e.store.UpdateSelectorHealth(ctx, m.ID, false)
			if m.IsRequired {
				return fmt.Errorf("fill required field %q: %w", m.ProfileField, err)
			}
			continue
		}

		e.logAction(ctx, job, platform, models.ApplicationLogEntry{
			Action: "fill_field", Detail: m.ProfileField, Selector: usedSelector,
			Success: true, StepNumber: stepNum,
		})
		_ = e.store.UpdateSelectorHealth(ctx, m.ID, true)

		e.pause(platform.WaitBetweenFieldsMs)
	}
	return nil
}

// applyField performs the input method for one mapping.
func (e *Engine) applyField(page *rod.Page, el *rod.Element, m models.FieldMapping, data map[string]string) error {
	_ = el.ScrollIntoView()
	value := data[m.ProfileField]

	switch m.InputMethod {
	case "type", "textarea":
		if value == "" {
			return nil
		}
		return e.typeHuman(el, value)
	case "select":
		opt := m.SelectValue
		if opt == "" {
			opt = value
		}
		if opt == "" {
			return nil
		}
		return el.Select([]string{opt}, true, rod.SelectorTypeText)
	case "upload":
		if value == "" {
			return fmt.Errorf("no file path for %q", m.ProfileField)
		}
		if _, err := os.Stat(value); err != nil {
			return fmt.Errorf("cv file %q: %w", value, err)
		}
		return el.SetFiles([]string{value})
	case "click":
		return el.Click(proto.InputMouseButtonLeft, 1)
	case "custom":
		if m.CustomScript == "" {
			return fmt.Errorf("custom input method without custom_script")
		}
		_, err := page.Eval(m.CustomScript)
		return err
	default:
		return fmt.Errorf("unknown input_method %q", m.InputMethod)
	}
}

// findElement resolves a mapping to a page element, trying the primary
// selector, then fallbacks, then a label-text lookup. It returns the element,
// the selector that worked, and whether one was found.
func (e *Engine) findElement(page *rod.Page, m models.FieldMapping) (*rod.Element, string, bool) {
	selectors := append([]string{m.CSSSelector}, m.FallbackSelectors...)
	for _, sel := range selectors {
		if sel == "" {
			continue
		}
		if el, err := page.Timeout(3 * time.Second).Element(sel); err == nil {
			return el.CancelTimeout(), sel, true
		}
	}
	if m.LabelText != "" {
		xpath := fmt.Sprintf(`//label[contains(normalize-space(.), %s)]//following::*[self::input or self::textarea or self::select][1]`, xpathLiteral(m.LabelText))
		if el, err := page.Timeout(3 * time.Second).ElementX(xpath); err == nil {
			return el.CancelTimeout(), "label:" + m.LabelText, true
		}
	}
	return nil, "", false
}

// advance clicks the "next" button for stepNum and waits for the next step's
// arrival indicator.
func (e *Engine) advance(ctx context.Context, page *rod.Page, steps []models.PlatformStep, stepNum int) error {
	var cur *models.PlatformStep
	for i := range steps {
		if steps[i].StepNumber == stepNum {
			cur = &steps[i]
			break
		}
	}
	if cur == nil || cur.NextSelector == "" {
		return fmt.Errorf("no next selector for step %d", stepNum)
	}
	btn, err := page.Timeout(5 * time.Second).Element(cur.NextSelector)
	if err != nil {
		return fmt.Errorf("next button %q: %w", cur.NextSelector, err)
	}
	if err := btn.CancelTimeout().Click(proto.InputMouseButtonLeft, 1); err != nil {
		return err
	}
	e.pause(cur.WaitMs)

	// Wait for the next step to arrive.
	for i := range steps {
		if steps[i].StepNumber == stepNum+1 && steps[i].ArrivalIndicator != "" {
			if _, err := page.Timeout(10 * time.Second).Element(steps[i].ArrivalIndicator); err != nil {
				return fmt.Errorf("next step did not arrive: %w", err)
			}
		}
	}
	return nil
}

// answerCommonQuestions scans visible labels for known question patterns and
// fills the configured default answers. Failures here are non-fatal.
func (e *Engine) answerCommonQuestions(ctx context.Context, page *rod.Page, job *models.Job, platform *models.Platform) {
	questions, err := e.store.GetCommonQuestions(ctx, &platform.ID)
	if err != nil {
		e.log.Warn("get common questions failed", "err", err)
		return
	}
	for _, q := range questions {
		re, err := regexp.Compile(q.QuestionPattern)
		if err != nil {
			continue
		}
		labels, err := page.Elements("label")
		if err != nil {
			return
		}
		for _, lbl := range labels {
			txt, err := lbl.Text()
			if err != nil || !re.MatchString(txt) {
				continue
			}
			if e.fillQuestion(page, lbl, q) {
				e.logAction(ctx, job, platform, models.ApplicationLogEntry{
					Action: "fill_field", Detail: "common_question: " + truncate(txt, 60), Success: true,
				})
			}
			break
		}
	}
}

func (e *Engine) fillQuestion(page *rod.Page, label *rod.Element, q models.CommonQuestion) bool {
	// Find the input/select associated with this label.
	input, err := label.ElementX(`.//following::*[self::input or self::textarea or self::select][1]`)
	if err != nil {
		return false
	}
	switch q.AnswerType {
	case "select":
		return input.Select([]string{q.Answer}, true, rod.SelectorTypeText) == nil
	default:
		_ = input.ScrollIntoView()
		return e.typeHuman(input, q.Answer) == nil
	}
}

// submit clicks the submit control and waits for the success indicator.
func (e *Engine) submit(ctx context.Context, page *rod.Page, job *models.Job, platform *models.Platform) error {
	btn, err := page.Timeout(5 * time.Second).Element(submitSelector)
	if err != nil {
		return fmt.Errorf("submit button not found: %w", err)
	}
	_ = btn.ScrollIntoView()
	if err := btn.CancelTimeout().Click(proto.InputMouseButtonLeft, 1); err != nil {
		return fmt.Errorf("click submit: %w", err)
	}
	e.logAction(ctx, job, platform, models.ApplicationLogEntry{Action: "submit", Success: true})

	if platform.SuccessIndicator == "" {
		// No indicator configured: give the page a moment and assume success.
		e.pause(2000)
		return nil
	}
	if _, err := page.Timeout(10 * time.Second).Element(platform.SuccessIndicator); err != nil {
		return fmt.Errorf("success indicator %q not seen: %w", platform.SuccessIndicator, err)
	}
	return nil
}

const submitSelector = `button[type="submit"], input[type="submit"], #submit_app, .template-btn-submit, button#js-application-submit`

// ---------------------------------------------------------------------------
// Browser lifecycle
// ---------------------------------------------------------------------------

func (e *Engine) ensureBrowser() (*rod.Browser, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.browser != nil {
		return e.browser, nil
	}

	l := launcher.New().Headless(e.headless).
		Set("no-sandbox").
		Set("disable-dev-shm-usage").
		Set("disable-gpu")
	if path, ok := launcher.LookPath(); ok {
		l = l.Bin(path)
	}
	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("launch chromium: %w", err)
	}
	b := rod.New().ControlURL(controlURL)
	if err := b.Connect(); err != nil {
		return nil, fmt.Errorf("connect browser: %w", err)
	}
	e.browser = b
	return b, nil
}

// openPage creates a page, navigates to url and waits for load. cleanup closes
// the page (but keeps the shared browser alive for reuse).
func (e *Engine) openPage(ctx context.Context, url string) (*rod.Page, func(), error) {
	b, err := e.ensureBrowser()
	if err != nil {
		return nil, nil, err
	}
	page, err := b.Page(proto.TargetCreateTarget{})
	if err != nil {
		return nil, nil, fmt.Errorf("create page: %w", err)
	}
	page = page.Context(ctx)
	if err := page.Navigate(url); err != nil {
		_ = page.Close()
		return nil, nil, fmt.Errorf("navigate: %w", err)
	}
	if err := page.WaitLoad(); err != nil {
		e.log.Warn("wait load", "err", err)
	}
	cleanup := func() { _ = page.Close() }
	return page, cleanup, nil
}

// Close shuts down the shared browser.
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.browser != nil {
		err := e.browser.Close()
		e.browser = nil
		return err
	}
	return nil
}

// runAction executes a single pre-action.
func (e *Engine) runAction(page *rod.Page, a models.Action) error {
	switch a.Type {
	case "click":
		el, err := page.Timeout(5 * time.Second).Element(a.Selector)
		if err != nil {
			return err
		}
		if err := el.CancelTimeout().Click(proto.InputMouseButtonLeft, 1); err != nil {
			return err
		}
	case "scroll":
		el, err := page.Timeout(5 * time.Second).Element(a.Selector)
		if err != nil {
			return err
		}
		return el.CancelTimeout().ScrollIntoView()
	case "eval":
		_, err := page.Eval(a.Value)
		return err
	case "wait":
		// handled below
	}
	if a.WaitMs > 0 {
		e.pause(a.WaitMs)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// typeHuman clears the field then types value character-by-character with small
// random delays to mimic a human.
func (e *Engine) typeHuman(el *rod.Element, value string) error {
	if err := el.Focus(); err != nil {
		return err
	}
	// Clear existing content.
	_ = el.SelectAllText()
	_ = el.Input("")
	for _, r := range value {
		if err := el.Input(string(r)); err != nil {
			return err
		}
		time.Sleep(time.Duration(50+rand.Intn(100)) * time.Millisecond)
	}
	return nil
}

// pause sleeps for ms plus a little jitter (200-500ms when ms is 0/small).
func (e *Engine) pause(ms int) {
	if ms <= 0 {
		ms = 200 + rand.Intn(300)
	} else {
		ms += rand.Intn(ms/2 + 1)
	}
	time.Sleep(time.Duration(ms) * time.Millisecond)
}

// screenshot captures the page to a PNG and returns its path ("" on failure).
func (e *Engine) screenshot(page *rod.Page, job *models.Job, label string) string {
	if err := os.MkdirAll(e.screenshotDir, 0o755); err != nil {
		e.log.Warn("mkdir screenshots", "err", err)
		return ""
	}
	name := fmt.Sprintf("%s_%s_%d.png", sanitize(job.ID), label, time.Now().Unix())
	path := filepath.Join(e.screenshotDir, name)
	data, err := page.Screenshot(false, nil)
	if err != nil {
		e.log.Warn("screenshot", "err", err)
		return ""
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		e.log.Warn("write screenshot", "err", err)
		return ""
	}
	return path
}

// logAction writes an audit-log row, filling in job/platform/timestamp.
func (e *Engine) logAction(ctx context.Context, job *models.Job, platform *models.Platform, entry models.ApplicationLogEntry) {
	entry.JobID = job.ID
	if platform != nil {
		pid := platform.ID
		entry.PlatformID = &pid
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	if err := e.store.LogApplicationAction(ctx, entry); err != nil {
		e.log.Warn("log application action failed", "err", err)
	}
}

func sanitize(s string) string {
	return strings.NewReplacer("/", "_", ":", "_", "\\", "_", " ", "_").Replace(s)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// xpathLiteral quotes s for safe embedding in an XPath expression, handling
// strings that contain quotes via concat().
func xpathLiteral(s string) string {
	if !strings.Contains(s, "'") {
		return "'" + s + "'"
	}
	if !strings.Contains(s, `"`) {
		return `"` + s + `"`
	}
	parts := strings.Split(s, "'")
	for i := range parts {
		parts[i] = "'" + parts[i] + "'"
	}
	return "concat(" + strings.Join(parts, `, "'", `) + ")"
}

// Ensure Engine satisfies Applier.
var _ Applier = (*Engine)(nil)
