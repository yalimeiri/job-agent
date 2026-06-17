package matcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/models"
	"github.com/yalimeiri/job-agent/internal/ratelimit"
	"github.com/yalimeiri/job-agent/internal/retry"
)

// OpenAIMatcher talks to any OpenAI-compatible /chat/completions endpoint:
// Groq, Google Gemini's compat endpoint, OpenRouter, a local Ollama, etc.
type OpenAIMatcher struct {
	apiKey     string
	baseURL    string // e.g. https://api.groq.com/openai/v1
	scoreModel string
	coverModel string
	minScore   float64
	client     *http.Client
	limiter    *ratelimit.Limiter
	log        *slog.Logger
}

// NewOpenAICompatible builds an OpenAI-compatible matcher from the config.
func NewOpenAICompatible(cfg config.MatchingConfig, log *slog.Logger, opts ...Option) (*OpenAIMatcher, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("openai-compatible matcher: api_key is required")
	}
	if cfg.BaseURL == "" {
		return nil, fmt.Errorf("openai-compatible matcher: base_url is required")
	}
	if log == nil {
		log = slog.Default()
	}
	m := &OpenAIMatcher{
		apiKey:     cfg.APIKey,
		baseURL:    strings.TrimRight(cfg.BaseURL, "/"),
		scoreModel: cfg.Model,
		coverModel: cfg.CoverLetterModel,
		minScore:   cfg.MinScore,
		client:     &http.Client{Timeout: 60 * time.Second},
		limiter:    ratelimit.PerMinute("openai-compatible", claudeRPM),
		log:        log.With("component", "matcher", "provider", cfg.Provider),
	}
	for _, o := range opts {
		o(m)
	}
	return m, nil
}

func (m *OpenAIMatcher) setClient(c *http.Client)        { m.client = c }
func (m *OpenAIMatcher) setBaseURL(u string)             { m.baseURL = strings.TrimRight(u, "/") }
func (m *OpenAIMatcher) setLimiter(l *ratelimit.Limiter) { m.limiter = l }

// Match scores the job and, when relevant, generates a cover letter.
func (m *OpenAIMatcher) Match(ctx context.Context, job *models.Job, profile config.ProfileConfig) (*models.MatchResult, error) {
	system, user := buildScorePrompt(job, profile)
	text, err := m.call(ctx, m.scoreModel, system, user, 1024, true)
	if err != nil {
		return nil, err
	}
	res, err := parseScore(text)
	if err != nil {
		return nil, err
	}
	res.Relevant = res.Score >= m.minScore

	if res.Relevant {
		cs, cu := buildCoverLetterPrompt(job, profile, res)
		letter, err := m.call(ctx, m.coverModel, cs, cu, 1024, false)
		if err != nil {
			m.log.Warn("cover letter generation failed", "job", job.ID, "err", err)
		} else {
			res.CoverLetter = strings.TrimSpace(letter)
		}
	}
	return res, nil
}

// chatRequest / chatResponse model the OpenAI chat-completions schema.
type chatRequest struct {
	Model          string        `json:"model"`
	Messages       []chatMessage `json:"messages"`
	Temperature    float64       `json:"temperature"`
	MaxTokens      int           `json:"max_tokens"`
	ResponseFormat *respFormat   `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type respFormat struct {
	Type string `json:"type"` // "json_object"
}

type chatResponse struct {
	Choices []struct {
		Message chatMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// call posts a chat-completions request with retry on 429/5xx. When jsonMode is
// true it asks for a JSON object response (supported by Groq/OpenAI/Gemini).
func (m *OpenAIMatcher) call(ctx context.Context, model, system, user string, maxTokens int, jsonMode bool) (string, error) {
	if m.limiter != nil {
		if err := m.limiter.Wait(ctx); err != nil {
			return "", fmt.Errorf("matcher rate limit: %w", err)
		}
	}

	reqStruct := chatRequest{
		Model:       model,
		MaxTokens:   maxTokens,
		Temperature: 0.4,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
	}
	if jsonMode {
		reqStruct.ResponseFormat = &respFormat{Type: "json_object"}
	}
	reqBody, err := json.Marshal(reqStruct)
	if err != nil {
		return "", err
	}
	endpoint := m.baseURL + "/chat/completions"

	var out string
	err = retry.Do(ctx, retry.DefaultPolicy(), func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(reqBody))
		if err != nil {
			return retry.Permanent(err)
		}
		req.Header.Set("Authorization", "Bearer "+m.apiKey)
		req.Header.Set("Content-Type", "application/json")

		resp, err := m.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)

		switch {
		case resp.StatusCode == http.StatusOK:
			var cr chatResponse
			if err := json.Unmarshal(body, &cr); err != nil {
				return retry.Permanent(fmt.Errorf("decode response: %w", err))
			}
			if cr.Error != nil {
				return retry.Permanent(fmt.Errorf("provider error: %s", cr.Error.Message))
			}
			if len(cr.Choices) == 0 {
				return retry.Permanent(fmt.Errorf("no choices in response"))
			}
			out = cr.Choices[0].Message.Content
			return nil
		case resp.StatusCode == http.StatusTooManyRequests:
			return retry.RetryAfter{Err: fmt.Errorf("matcher 429"), Delay: retryAfter(resp)}
		case resp.StatusCode >= 500:
			return fmt.Errorf("matcher %d: %s", resp.StatusCode, truncate(string(body), 200))
		default:
			return retry.Permanent(fmt.Errorf("matcher %d: %s", resp.StatusCode, truncate(string(body), 200)))
		}
	})
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "", fmt.Errorf("matcher returned empty content")
	}
	return out, nil
}

// Ensure OpenAIMatcher satisfies Matcher.
var _ Matcher = (*OpenAIMatcher)(nil)
