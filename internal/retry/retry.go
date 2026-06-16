// Package retry provides a small, dependency-free retry helper with
// exponential backoff and jitter. It is context-aware: a cancelled context
// aborts the retry loop immediately.
package retry

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"time"
)

// Policy configures the backoff behaviour.
type Policy struct {
	MaxAttempts int           // total attempts including the first (default 3)
	BaseDelay   time.Duration // delay before the first retry (default 500ms)
	MaxDelay    time.Duration // cap on any single delay (default 30s)
	Jitter      float64       // fraction of the delay applied as +/- jitter (default 0.3)
}

// DefaultPolicy matches the agent's behaviour spec: 3 attempts, 500ms base.
func DefaultPolicy() Policy {
	return Policy{MaxAttempts: 3, BaseDelay: 500 * time.Millisecond, MaxDelay: 30 * time.Second, Jitter: 0.3}
}

// permanentError wraps an error so Do stops retrying immediately.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent marks err as non-retryable. Returning Permanent(err) from the
// function passed to Do stops the retry loop and returns the underlying error.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err}
}

// RetryAfter wraps an error to request a specific delay before the next attempt
// (e.g. honouring an HTTP Retry-After header).
type RetryAfter struct {
	Err   error
	Delay time.Duration
}

func (e RetryAfter) Error() string { return e.Err.Error() }
func (e RetryAfter) Unwrap() error { return e.Err }

// Do runs fn, retrying on error per the policy. It returns the last error if
// all attempts fail, or ctx.Err() if the context is cancelled while waiting.
func Do(ctx context.Context, p Policy, fn func(ctx context.Context) error) error {
	if p.MaxAttempts <= 0 {
		p.MaxAttempts = 3
	}
	if p.BaseDelay <= 0 {
		p.BaseDelay = 500 * time.Millisecond
	}
	if p.MaxDelay <= 0 {
		p.MaxDelay = 30 * time.Second
	}

	var lastErr error
	for attempt := 0; attempt < p.MaxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}

		err := fn(ctx)
		if err == nil {
			return nil
		}
		lastErr = err

		// Permanent errors stop the loop.
		var perm permanentError
		if errors.As(err, &perm) {
			return perm.err
		}

		// Last attempt: don't sleep, just return.
		if attempt == p.MaxAttempts-1 {
			break
		}

		delay := p.backoff(attempt)
		var ra RetryAfter
		if errors.As(err, &ra) && ra.Delay > 0 {
			delay = ra.Delay
		}

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return lastErr
}

// backoff computes the delay for a given attempt with exponential growth and
// symmetric jitter, capped at MaxDelay.
func (p Policy) backoff(attempt int) time.Duration {
	d := float64(p.BaseDelay) * math.Pow(2, float64(attempt))
	if d > float64(p.MaxDelay) {
		d = float64(p.MaxDelay)
	}
	if p.Jitter > 0 {
		// jitter in [-p.Jitter, +p.Jitter] of d
		j := (rand.Float64()*2 - 1) * p.Jitter
		d = d * (1 + j)
	}
	if d < 0 {
		d = 0
	}
	return time.Duration(d)
}
