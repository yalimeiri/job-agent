package matcher

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/models"
	"github.com/yalimeiri/job-agent/internal/ratelimit"
)

func TestExtractJSON(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{`{"score":0.9}`, `{"score":0.9}`},
		{"```json\n{\"score\":0.5}\n```", `{"score":0.5}`},
		{"Here is the result: {\"score\":0.1} thanks", `{"score":0.1}`},
		{"prefix {\"a\":{\"b\":1}} suffix", `{"a":{"b":1}}`},
	}
	for _, tt := range tests {
		if got := extractJSON(tt.in); got != tt.want {
			t.Errorf("extractJSON(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// respond writes an Anthropic-style messages response containing text.
func respond(t *testing.T, w http.ResponseWriter, text string) {
	t.Helper()
	resp := anthropicResponse{}
	resp.Content = append(resp.Content, struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}{Type: "text", Text: text})
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(resp)
}

func TestMatchScoresAndWritesCoverLetter(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "sk-test" {
			t.Errorf("missing x-api-key header")
		}
		if r.Header.Get("anthropic-version") == "" {
			t.Errorf("missing anthropic-version header")
		}
		calls++
		switch calls {
		case 1: // scoring
			respond(t, w, `{"score":0.82,"relevant":true,"reasoning":"Strong Go fit","key_skill_gaps":["k8s"],"strengths":["Go","React"]}`)
		default: // cover letter
			respond(t, w, "Dear Hiring Manager, I am excited to apply...")
		}
	}))
	defer srv.Close()

	m, err := NewClaude(config.MatchingConfig{
		APIKey: "sk-test", Model: "haiku", CoverLetterModel: "sonnet", MinScore: 0.6,
	}, nil, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithLimiter(ratelimit.PerMinute("t", 1000)))
	if err != nil {
		t.Fatal(err)
	}

	res, err := m.Match(context.Background(), &models.Job{Title: "Go Dev", Company: "Acme"}, config.ProfileConfig{FullName: "Jane"})
	if err != nil {
		t.Fatalf("Match() = %v", err)
	}
	if res.Score != 0.82 {
		t.Errorf("score = %v, want 0.82", res.Score)
	}
	if !res.Relevant {
		t.Error("expected relevant=true")
	}
	if res.CoverLetter == "" {
		t.Error("expected cover letter to be generated for relevant job")
	}
	if calls != 2 {
		t.Errorf("expected 2 API calls (score + cover), got %d", calls)
	}
}

func TestMatchBelowThresholdSkipsCoverLetter(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		respond(t, w, `{"score":0.3,"relevant":false,"reasoning":"weak fit"}`)
	}))
	defer srv.Close()

	m, _ := NewClaude(config.MatchingConfig{APIKey: "sk-test", Model: "h", CoverLetterModel: "s", MinScore: 0.6},
		nil, WithBaseURL(srv.URL), WithHTTPClient(srv.Client()), WithLimiter(ratelimit.PerMinute("t", 1000)))

	res, err := m.Match(context.Background(), &models.Job{Title: "PHP Dev"}, config.ProfileConfig{})
	if err != nil {
		t.Fatalf("Match() = %v", err)
	}
	if res.Relevant {
		t.Error("expected not relevant")
	}
	if res.CoverLetter != "" {
		t.Error("should not generate cover letter below threshold")
	}
	if calls != 1 {
		t.Errorf("expected 1 API call, got %d", calls)
	}
}
