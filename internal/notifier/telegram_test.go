package notifier

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yalimeiri/job-agent/internal/models"
)

func TestSendReport(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "/bottoken123/sendMessage") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	tg, err := NewTelegram(map[string]string{"bot_token": "token123", "chat_id": "42"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tg.baseURL = srv.URL
	tg.client = srv.Client()

	report := &models.CycleReport{
		StartedAt:  time.Now().Add(-time.Minute),
		FinishedAt: time.Now(),
		Scraped:    10, NewJobs: 4, Matched: 2, Rejected: 2, Applied: 1, DryRun: true,
		TopMatches: []*models.Job{{Title: "Go Dev <x>", Company: "Acme & Co", MatchScore: 0.91, ApplyURL: "https://x/y"}},
	}
	if err := tg.SendReport(context.Background(), report); err != nil {
		t.Fatalf("SendReport() = %v", err)
	}
	var payload struct {
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	if err := json.Unmarshal([]byte(gotBody), &payload); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if payload.ChatID != "42" {
		t.Errorf("chat_id = %q, want 42", payload.ChatID)
	}
	// HTML escaping applied to titles/companies.
	if !strings.Contains(payload.Text, "Go Dev &lt;x&gt;") || !strings.Contains(payload.Text, "Acme &amp; Co") {
		t.Errorf("expected escaped HTML in text: %s", payload.Text)
	}
}

func TestNewTelegramRequiresCreds(t *testing.T) {
	if _, err := NewTelegram(map[string]string{"bot_token": "x"}, nil); err == nil {
		t.Fatal("expected error when chat_id missing")
	}
}
