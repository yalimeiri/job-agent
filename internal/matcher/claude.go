package matcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/models"
	"github.com/yalimeiri/job-agent/internal/ratelimit"
	"github.com/yalimeiri/job-agent/internal/retry"
)

const (
	anthropicURL        = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion = "2023-06-01"
	// claudeRPM is the Claude API request-per-minute budget from the spec.
	claudeRPM = 50
)

// ClaudeMatcher scores jobs with a fast model and, for relevant jobs, drafts a
// tailored cover letter with a stronger model.
type ClaudeMatcher struct {
	apiKey          string
	scoreModel      string
	coverModel      string
	minScore        float64
	baseURL         string // overridable for tests
	client          *http.Client
	limiter         *ratelimit.Limiter
	log             *slog.Logger
}

// Option customises a ClaudeMatcher.
type Option func(*ClaudeMatcher)

// WithHTTPClient overrides the HTTP client (used in tests).
func WithHTTPClient(c *http.Client) Option { return func(m *ClaudeMatcher) { m.client = c } }

// WithBaseURL overrides the API endpoint (used in tests).
func WithBaseURL(u string) Option { return func(m *ClaudeMatcher) { m.baseURL = u } }

// WithLimiter overrides the rate limiter.
func WithLimiter(l *ratelimit.Limiter) Option { return func(m *ClaudeMatcher) { m.limiter = l } }

// NewClaude builds a Claude-backed matcher from the matching config.
func NewClaude(cfg config.MatchingConfig, log *slog.Logger, opts ...Option) (*ClaudeMatcher, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("claude matcher: api_key is required")
	}
	if log == nil {
		log = slog.Default()
	}
	m := &ClaudeMatcher{
		apiKey:     cfg.APIKey,
		scoreModel: cfg.Model,
		coverModel: cfg.CoverLetterModel,
		minScore:   cfg.MinScore,
		baseURL:    anthropicURL,
		client:     &http.Client{Timeout: 60 * time.Second},
		limiter:    ratelimit.PerMinute("claude", claudeRPM),
		log:        log.With("component", "matcher"),
	}
	for _, o := range opts {
		o(m)
	}
	return m, nil
}

// Match scores the job; if it clears minScore a cover letter is generated.
func (m *ClaudeMatcher) Match(ctx context.Context, job *models.Job, profile config.ProfileConfig) (*models.MatchResult, error) {
	res, err := m.score(ctx, job, profile)
	if err != nil {
		return nil, err
	}
	res.Relevant = res.Score >= m.minScore

	if res.Relevant {
		letter, err := m.coverLetter(ctx, job, profile, res)
		if err != nil {
			// A missing cover letter shouldn't fail the match; log and continue.
			m.log.Warn("cover letter generation failed", "job", job.ID, "err", err)
		} else {
			res.CoverLetter = letter
		}
	}
	return res, nil
}

// scoreResponse is the JSON schema we instruct Claude to return for scoring.
type scoreResponse struct {
	Score        float64  `json:"score"`
	Relevant     bool     `json:"relevant"`
	Reasoning    string   `json:"reasoning"`
	KeySkillGaps []string `json:"key_skill_gaps"`
	Strengths    []string `json:"strengths"`
}

func (m *ClaudeMatcher) score(ctx context.Context, job *models.Job, profile config.ProfileConfig) (*models.MatchResult, error) {
	system := "You are a meticulous technical recruiter. Evaluate how well a candidate " +
		"fits a job. Respond with ONLY a single JSON object, no markdown, no code fences. " +
		"Schema: {\"score\": number 0.0-1.0, \"relevant\": boolean, \"reasoning\": string, " +
		"\"key_skill_gaps\": string[], \"strengths\": string[]}. " +
		"score reflects overall fit considering role match, skills, seniority and location."

	user := fmt.Sprintf(`CANDIDATE PROFILE
Name: %s
Years of experience: %d
Target roles: %s
Skills: %s
Summary: %s

JOB
Title: %s
Company: %s
Location: %s (remote=%t)
Type: %s
Requirements: %s
Description:
%s`,
		profile.FullName, profile.YearsExperience,
		strings.Join(profile.TargetRoles, ", "), strings.Join(profile.Skills, ", "),
		profile.Summary,
		job.Title, job.Company, job.Location, job.Remote, job.JobType,
		truncate(job.Requirements, 2000), truncate(job.Description, 4000))

	text, err := m.call(ctx, m.scoreModel, system, user, 1024)
	if err != nil {
		return nil, err
	}

	var sr scoreResponse
	if err := json.Unmarshal([]byte(extractJSON(text)), &sr); err != nil {
		return nil, fmt.Errorf("parse score JSON: %w (raw: %s)", err, truncate(text, 300))
	}
	if sr.Score < 0 {
		sr.Score = 0
	}
	if sr.Score > 1 {
		sr.Score = 1
	}
	return &models.MatchResult{
		Score:        sr.Score,
		Reasoning:    sr.Reasoning,
		KeySkillGaps: sr.KeySkillGaps,
		Strengths:    sr.Strengths,
	}, nil
}

func (m *ClaudeMatcher) coverLetter(ctx context.Context, job *models.Job, profile config.ProfileConfig, res *models.MatchResult) (string, error) {
	system := "You are an expert career writer. Write a concise, specific, professional " +
		"cover letter (180-250 words) in the first person. No placeholders, no markdown, " +
		"plain text only. Do not invent facts beyond the candidate profile."

	user := fmt.Sprintf(`Write a cover letter for this application.

CANDIDATE
Name: %s
Years of experience: %d
Skills: %s
Summary: %s
Strengths for this role: %s

JOB
Title: %s at %s
Location: %s
Requirements: %s`,
		profile.FullName, profile.YearsExperience, strings.Join(profile.Skills, ", "),
		profile.Summary, strings.Join(res.Strengths, ", "),
		job.Title, job.Company, job.Location, truncate(job.Requirements, 1500))

	text, err := m.call(ctx, m.coverModel, system, user, 1024)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

// ---------------------------------------------------------------------------
// Anthropic Messages API plumbing
// ---------------------------------------------------------------------------

type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    string             `json:"system,omitempty"`
	Messages  []anthropicMessage `json:"messages"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// call performs a single Messages API request with retry on 429/5xx and
// returns the concatenated text content.
func (m *ClaudeMatcher) call(ctx context.Context, model, system, user string, maxTokens int) (string, error) {
	if m.limiter != nil {
		if err := m.limiter.Wait(ctx); err != nil {
			return "", fmt.Errorf("claude rate limit: %w", err)
		}
	}

	reqBody, err := json.Marshal(anthropicRequest{
		Model:     model,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  []anthropicMessage{{Role: "user", Content: user}},
	})
	if err != nil {
		return "", err
	}

	var out string
	err = retry.Do(ctx, retry.DefaultPolicy(), func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL, bytes.NewReader(reqBody))
		if err != nil {
			return retry.Permanent(err)
		}
		req.Header.Set("x-api-key", m.apiKey)
		req.Header.Set("anthropic-version", anthropicAPIVersion)
		req.Header.Set("content-type", "application/json")

		resp, err := m.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		switch {
		case resp.StatusCode == http.StatusOK:
			var ar anthropicResponse
			if err := json.Unmarshal(body, &ar); err != nil {
				return retry.Permanent(fmt.Errorf("decode response: %w", err))
			}
			if ar.Error != nil {
				return retry.Permanent(fmt.Errorf("anthropic error: %s", ar.Error.Message))
			}
			var b strings.Builder
			for _, c := range ar.Content {
				if c.Type == "text" {
					b.WriteString(c.Text)
				}
			}
			out = b.String()
			return nil
		case resp.StatusCode == http.StatusTooManyRequests:
			return retry.RetryAfter{Err: fmt.Errorf("claude 429"), Delay: retryAfter(resp)}
		case resp.StatusCode >= 500:
			return fmt.Errorf("claude %d: %s", resp.StatusCode, truncate(string(body), 200))
		default:
			return retry.Permanent(fmt.Errorf("claude %d: %s", resp.StatusCode, truncate(string(body), 200)))
		}
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("claude returned empty content")
	}
	return out, nil
}

func retryAfter(resp *http.Response) time.Duration {
	if v := resp.Header.Get("Retry-After"); v != "" {
		if secs, err := strconv.Atoi(v); err == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

// extractJSON pulls the first balanced JSON object out of s, tolerating stray
// markdown fences or prose around it.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start >= 0 && end > start {
		return s[start : end+1]
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

// Ensure ClaudeMatcher satisfies Matcher.
var _ Matcher = (*ClaudeMatcher)(nil)
