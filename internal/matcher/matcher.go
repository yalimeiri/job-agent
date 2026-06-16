// Package matcher evaluates how well a job fits the user's profile. The
// production implementation uses the Anthropic Claude API; the interface keeps
// the rest of the agent decoupled from that choice (and easy to fake in tests).
package matcher

import (
	"context"

	"github.com/yalimeiri/job-agent/internal/config"
	"github.com/yalimeiri/job-agent/internal/models"
)

// Matcher evaluates job relevance against the user profile.
type Matcher interface {
	Match(ctx context.Context, job *models.Job, profile config.ProfileConfig) (*models.MatchResult, error)
}
