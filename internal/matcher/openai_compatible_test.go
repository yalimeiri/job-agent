package matcher

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/models"
	"github.com/yalimeiri/job-agent/internal/ratelimit"
)

// chatRespond writes an OpenAI chat-completions style response.
func chatRespond(t *testing.T, w http.ResponseWriter, content string) {
	t.Helper()
	var cr chatResponse
	cr.Choices = append(cr.Choices, struct {
		Message chatMessage `json:"message"`
	}{Message: chatMessage{Role: "assistant", Content: content}})
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(cr)
}

func TestOpenAIMatchScoresAndWritesCoverLetter(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer gsk-test" {
			t.Errorf("missing/invalid bearer auth: %q", r.Header.Get("Authorization"))
		}
		body, _ := io.ReadAll(r.Body)
		calls++
		switch calls {
		case 1:
			// First call must request JSON mode for scoring.
			if !strings.Contains(string(body), `"response_format"`) {
				t.Errorf("scoring call should set response_format: %s", body)
			}
			chatRespond(t, w, `{"score":0.78,"relevant":true,"reasoning":"Solid match","key_skill_gaps":["k8s"],"strengths":["Go","React"]}`)
		default:
			chatRespond(t, w, "Dear Hiring Manager, I'm excited to apply...")
		}
	}))
	defer srv.Close()

	m, err := NewOpenAICompatible(config.MatchingConfig{
		Provider: "groq", APIKey: "gsk-test", BaseURL: srv.URL,
		Model: "llama-3.3-70b-versatile", CoverLetterModel: "llama-3.3-70b-versatile", MinScore: 0.6,
	}, nil, WithHTTPClient(srv.Client()), WithLimiter(ratelimit.PerMinute("t", 1000)))
	if err != nil {
		t.Fatal(err)
	}

	res, err := m.Match(context.Background(), &models.Job{Title: "Go Dev", Company: "Acme"}, config.ProfileConfig{FullName: "Jane"})
	if err != nil {
		t.Fatalf("Match() = %v", err)
	}
	if res.Score != 0.78 || !res.Relevant {
		t.Errorf("score=%v relevant=%v, want 0.78/true", res.Score, res.Relevant)
	}
	if res.CoverLetter == "" {
		t.Error("expected cover letter for relevant job")
	}
	if calls != 2 {
		t.Errorf("expected 2 calls (score + cover), got %d", calls)
	}
}

func TestNewDispatcher(t *testing.T) {
	groq, err := New(config.MatchingConfig{Provider: "groq", APIKey: "x", BaseURL: "https://api.groq.com/openai/v1", Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := groq.(*OpenAIMatcher); !ok {
		t.Errorf("groq provider should yield *OpenAIMatcher, got %T", groq)
	}

	claude, err := New(config.MatchingConfig{Provider: "anthropic", APIKey: "x", Model: "m"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := claude.(*ClaudeMatcher); !ok {
		t.Errorf("anthropic provider should yield *ClaudeMatcher, got %T", claude)
	}
}
