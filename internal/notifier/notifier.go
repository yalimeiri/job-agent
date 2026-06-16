// Package notifier delivers cycle reports and alerts to the user. The default
// implementation targets Telegram, but the agent only depends on the interface.
package notifier

import (
	"context"

	"github.com/yalimeiri/job-agent/internal/models"
)

// Notifier sends reports and alerts.
type Notifier interface {
	SendReport(ctx context.Context, report *models.CycleReport) error
	SendAlert(ctx context.Context, message string) error
}

// Nop is a no-op notifier used when notifications are disabled.
type Nop struct{}

// SendReport implements Notifier.
func (Nop) SendReport(context.Context, *models.CycleReport) error { return nil }

// SendAlert implements Notifier.
func (Nop) SendAlert(context.Context, string) error { return nil }
