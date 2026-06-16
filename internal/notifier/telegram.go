package notifier

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

	"github.com/yalimeiri/job-agent/internal/models"
	"github.com/yalimeiri/job-agent/internal/retry"
)

// Telegram sends messages via the Telegram Bot API.
type Telegram struct {
	token   string
	chatID  string
	baseURL string // overridable for tests; defaults to https://api.telegram.org
	client  *http.Client
	log     *slog.Logger
}

// Config holds Telegram bot credentials.
type Config struct {
	BotToken string
	ChatID   string
}

// NewTelegram builds a Telegram notifier from a config map (bot_token, chat_id).
func NewTelegram(cfg map[string]string, log *slog.Logger) (*Telegram, error) {
	token := cfg["bot_token"]
	chatID := cfg["chat_id"]
	if token == "" || chatID == "" {
		return nil, fmt.Errorf("telegram notifier: bot_token and chat_id are required")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Telegram{
		token:   token,
		chatID:  chatID,
		baseURL: "https://api.telegram.org",
		client:  &http.Client{Timeout: 20 * time.Second},
		log:     log.With("component", "notifier"),
	}, nil
}

// SendReport formats and sends a cycle summary.
func (t *Telegram) SendReport(ctx context.Context, r *models.CycleReport) error {
	return t.send(ctx, formatReport(r))
}

// SendAlert sends an ad-hoc alert message.
func (t *Telegram) SendAlert(ctx context.Context, message string) error {
	return t.send(ctx, "⚠️ "+message)
}

func formatReport(r *models.CycleReport) string {
	var b strings.Builder
	mode := "LIVE"
	if r.DryRun {
		mode = "DRY-RUN"
	}
	fmt.Fprintf(&b, "<b>Job Agent cycle</b> (%s) — %s\n", mode, r.Duration().Round(time.Second))
	fmt.Fprintf(&b, "Scraped: %d | New: %d\n", r.Scraped, r.NewJobs)
	fmt.Fprintf(&b, "✅ Matched: %d | ❌ Rejected: %d\n", r.Matched, r.Rejected)
	fmt.Fprintf(&b, "📨 Applied: %d | ⚠️ Failed: %d | ⏭️ Skipped: %d\n", r.Applied, r.Failed, r.Skipped)

	if len(r.TopMatches) > 0 {
		b.WriteString("\n<b>Top matches</b>\n")
		for _, j := range r.TopMatches {
			fmt.Fprintf(&b, "• %.0f%% — %s @ %s\n  %s\n",
				j.MatchScore*100, escape(j.Title), escape(j.Company), escape(j.ApplyURL))
		}
	}
	if len(r.ScraperError) > 0 {
		b.WriteString("\n<b>Scraper errors</b>\n")
		for src, e := range r.ScraperError {
			fmt.Fprintf(&b, "• %s: %s\n", escape(src), escape(e))
		}
	}
	return b.String()
}

// send posts a message to the Telegram sendMessage endpoint with retry.
func (t *Telegram) send(ctx context.Context, text string) error {
	endpoint := fmt.Sprintf("%s/bot%s/sendMessage", t.baseURL, t.token)
	payload := map[string]any{
		"chat_id":                  t.chatID,
		"text":                     text,
		"parse_mode":               "HTML",
		"disable_web_page_preview": true,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}

	return retry.Do(ctx, retry.DefaultPolicy(), func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return retry.Permanent(err)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := t.client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		respBody, _ := io.ReadAll(resp.Body)

		switch {
		case resp.StatusCode == http.StatusOK:
			return nil
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			return fmt.Errorf("telegram %d: %s", resp.StatusCode, string(respBody))
		default:
			return retry.Permanent(fmt.Errorf("telegram %d: %s", resp.StatusCode, string(respBody)))
		}
	})
}

// escape sanitises text for Telegram HTML parse mode.
func escape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

// Ensure Telegram satisfies Notifier.
var _ Notifier = (*Telegram)(nil)
