// Package ratelimit provides per-source token-bucket rate limiters plus a
// simple monthly API budget tracker (used for the JSearch free-tier quota).
package ratelimit

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter wraps golang.org/x/time/rate with an optional hard monthly budget.
type Limiter struct {
	name    string
	limiter *rate.Limiter

	mu          sync.Mutex
	budget      int       // -1 = unlimited
	used        int       // calls used in the current window
	windowStart time.Time // start of the current monthly window
}

// NewLimiter creates a limiter allowing r events per second with the given
// burst. budget is the maximum number of Wait()/Allow() grants per calendar
// month; pass a value <= 0 for no monthly cap.
func NewLimiter(name string, r rate.Limit, burst, budget int) *Limiter {
	if budget <= 0 {
		budget = -1
	}
	return &Limiter{
		name:        name,
		limiter:     rate.NewLimiter(r, burst),
		budget:      budget,
		windowStart: monthStart(time.Now()),
	}
}

// PerMinute is a convenience constructor for an N-requests-per-minute limiter.
func PerMinute(name string, n int) *Limiter {
	return NewLimiter(name, rate.Every(time.Minute/time.Duration(n)), n, -1)
}

// PerMonth is a convenience constructor enforcing a monthly budget while still
// spreading calls (1 token every few seconds, small burst).
func PerMonth(name string, budget int) *Limiter {
	return NewLimiter(name, rate.Every(10*time.Second), 5, budget)
}

// ErrBudgetExceeded is returned when the monthly budget is exhausted.
var ErrBudgetExceeded = fmt.Errorf("monthly API budget exceeded")

// Wait blocks until a token is available or ctx is done, and consumes one unit
// of the monthly budget. It returns ErrBudgetExceeded if the budget is spent.
func (l *Limiter) Wait(ctx context.Context) error {
	if err := l.reserveBudget(); err != nil {
		return err
	}
	if err := l.limiter.Wait(ctx); err != nil {
		// Token not actually consumed from the bucket perspective; refund budget.
		l.refundBudget()
		return err
	}
	return nil
}

func (l *Limiter) reserveBudget() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rolloverLocked()
	if l.budget >= 0 && l.used >= l.budget {
		return fmt.Errorf("%s: %w (used %d/%d)", l.name, ErrBudgetExceeded, l.used, l.budget)
	}
	l.used++
	return nil
}

func (l *Limiter) refundBudget() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.used > 0 {
		l.used--
	}
}

// rolloverLocked resets the budget counter at the start of a new month.
func (l *Limiter) rolloverLocked() {
	now := time.Now()
	if start := monthStart(now); start.After(l.windowStart) {
		l.windowStart = start
		l.used = 0
	}
}

// Remaining reports how many budget units are left this month (-1 if unlimited).
func (l *Limiter) Remaining() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.rolloverLocked()
	if l.budget < 0 {
		return -1
	}
	return l.budget - l.used
}

func monthStart(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}

// Registry holds named limiters so the agent can share one per API source.
type Registry struct {
	mu       sync.RWMutex
	limiters map[string]*Limiter
}

// NewRegistry creates an empty limiter registry.
func NewRegistry() *Registry {
	return &Registry{limiters: map[string]*Limiter{}}
}

// Register stores a limiter under name.
func (r *Registry) Register(name string, l *Limiter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.limiters[name] = l
}

// Get returns the limiter for name, or nil if absent.
func (r *Registry) Get(name string) *Limiter {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.limiters[name]
}
