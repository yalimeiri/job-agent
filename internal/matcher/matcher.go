// Package matcher evaluates how well a job fits the user's profile. The
// production implementation uses the Anthropic Claude API; the interface keeps
// the rest of the agent decoupled from that choice (and easy to fake in tests).
package matcher

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/models"
)

// Matcher evaluates job relevance against the user profile.
type Matcher interface {
	Match(ctx context.Context, job *models.Job, profile config.ProfileConfig) (*models.MatchResult, error)
}

// New builds the matcher backend selected by cfg.Provider:
//   - "anthropic"                -> Claude (api.anthropic.com)
//   - "groq" / "openai_compatible" -> any OpenAI-compatible chat API
func New(cfg config.MatchingConfig, log *slog.Logger) (Matcher, error) {
	switch cfg.Provider {
	case "groq", "openai_compatible":
		return NewOpenAICompatible(cfg, log)
	case "anthropic", "":
		return NewClaude(cfg, log)
	default:
		return nil, fmt.Errorf("unknown matching provider %q", cfg.Provider)
	}
}
