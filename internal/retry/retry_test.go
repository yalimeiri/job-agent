package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDoSucceedsAfterRetries(t *testing.T) {
	p := Policy{MaxAttempts: 5, BaseDelay: time.Millisecond, MaxDelay: 5 * time.Millisecond, Jitter: 0}
	calls := 0
	err := Do(context.Background(), p, func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("transient")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do() = %v, want nil", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestDoExhaustsAttempts(t *testing.T) {
	p := Policy{MaxAttempts: 3, BaseDelay: time.Millisecond, Jitter: 0}
	calls := 0
	want := errors.New("always")
	err := Do(context.Background(), p, func(context.Context) error {
		calls++
		return want
	})
	if !errors.Is(err, want) {
		t.Fatalf("Do() = %v, want %v", err, want)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestPermanentStopsImmediately(t *testing.T) {
	p := Policy{MaxAttempts: 5, BaseDelay: time.Millisecond, Jitter: 0}
	calls := 0
	sentinel := errors.New("fatal")
	err := Do(context.Background(), p, func(context.Context) error {
		calls++
		return Permanent(sentinel)
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Do() = %v, want %v", err, sentinel)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1 (permanent should not retry)", calls)
	}
}

func TestContextCancellation(t *testing.T) {
	p := Policy{MaxAttempts: 10, BaseDelay: 50 * time.Millisecond, Jitter: 0}
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()
	err := Do(ctx, p, func(context.Context) error {
		calls++
		return errors.New("transient")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() = %v, want context.Canceled", err)
	}
}

func TestRetryAfterHint(t *testing.T) {
	p := Policy{MaxAttempts: 2, BaseDelay: time.Second, Jitter: 0}
	start := time.Now()
	calls := 0
	_ = Do(context.Background(), p, func(context.Context) error {
		calls++
		if calls == 1 {
			return RetryAfter{Err: errors.New("429"), Delay: 5 * time.Millisecond}
		}
		return nil
	})
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("RetryAfter not honoured, waited %v", elapsed)
	}
}
