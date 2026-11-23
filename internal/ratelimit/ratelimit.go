// Package ratelimit provides a token bucket and a throttled reader.
//
// The limiter is hand-rolled instead of using golang.org/x/time/rate so that
// tong keeps its dependency list to the S3 SDK and the command-line parser,
// and so that the waits are driven by an injected clock and are therefore
// deterministic in tests.
package ratelimit

import (
	"context"
	"io"
	"sync"
	"time"
)

// maxRead caps the size of a single read, so that a very large budget still
// hands out its bytes in chunks instead of one huge read.
const maxRead = 1 << 20

// Clock is the source of time the limiter waits on. Tests inject a fake
// clock to keep the waits deterministic.
type Clock interface {
	// Now returns the current time.
	Now() time.Time
	// Sleep waits for d to pass, or returns the error of ctx when the wait
	// is interrupted.
	Sleep(ctx context.Context, d time.Duration) error
}

// RealClock is the clock of the running process.
type RealClock struct{}

// Now returns the current time.
func (RealClock) Now() time.Time { return time.Now() }

// Sleep waits for d to pass, or returns early when ctx is done.
func (RealClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Limiter hands out bytes from a budget that refills at a fixed rate.
//
// A single limiter is shared by every transfer of a run, so the budget is
// global instead of per file. The limiter is safe for concurrent use.
type Limiter struct {
	clock Clock
	// rate is the budget in bytes per second.
	rate float64
	// capacity is the credit that may accrue while the limiter is idle: one
	// second of the budget.
	capacity float64

	mu sync.Mutex
	// tokens is the credit that has accrued but was not spent yet.
	tokens float64
	// last is the time the budget was last updated. It runs ahead of the
	// clock while the budget is in debt.
	last time.Time
}

// New returns a limiter that hands out rate bytes per second, or nil when
// rate is not positive, which means "no limit". A nil clock means the real
// clock.
func New(rate int64, clock Clock) *Limiter {
	if rate <= 0 {
		return nil
	}
	if clock == nil {
		clock = RealClock{}
	}
	return &Limiter{
		clock:    clock,
		rate:     float64(rate),
		capacity: float64(rate),
		last:     clock.Now(),
	}
}

// Wait spends n bytes from the budget, blocking until they are available. It
// returns the error of ctx when the wait is interrupted.
func (l *Limiter) Wait(ctx context.Context, n int) error {
	if n <= 0 {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	wait := l.reserve(n)
	if wait <= 0 {
		return nil
	}
	return l.clock.Sleep(ctx, wait)
}

// reserve spends n bytes from the budget and returns how long the caller has
// to wait for them.
//
// The wait itself happens outside the lock, so that one slow worker does not
// hold up the others and a cancelled worker does not block them either. The
// budget is still accounted for before the lock is released, because the
// reservation moves last into the future.
func (l *Limiter) reserve(n int) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.clock.Now()
	if l.last.After(now) {
		// A previous wait is still in the future, so no time has passed.
		now = l.last
	} else if elapsed := now.Sub(l.last); elapsed > 0 {
		// The budget refills while the limiter is idle, but only up to one
		// second's worth, so that a long pause does not unlock a burst.
		l.tokens = min(l.capacity, l.tokens+elapsed.Seconds()*l.rate)
	}
	l.last = now

	need := float64(n)
	if l.tokens >= need {
		l.tokens -= need
		return 0
	}

	// The budget is in debt by the missing bytes. The next call sees a
	// virtual time that is already that far ahead, so that the bytes of a
	// single read are spread over the budget instead of arriving as one
	// second's worth of data at a time.
	missing := need - l.tokens
	l.tokens = 0
	service := max(time.Duration(missing/l.rate*float64(time.Second)), 0)
	l.last = now.Add(service)
	return max(l.last.Sub(l.clock.Now()), 0)
}

// Reader wraps r so that every read spends its bytes from the budget, which
// pushes the backpressure up to the sender. A read returns the error of ctx
// when the wait is interrupted, so that a cancelled transfer stops.
func (l *Limiter) Reader(ctx context.Context, r io.Reader) io.Reader {
	return &reader{ctx: ctx, limiter: l, r: r}
}

// chunk returns the largest read the limiter lets through at once: one
// second of the budget, so that a slow budget blocks for at most a second
// at a time.
func (l *Limiter) chunk() int {
	return int(min(max(l.rate, 1), maxRead))
}

// reader is a throttled reader.
type reader struct {
	ctx     context.Context
	limiter *Limiter
	r       io.Reader
}

// Read reads from the underlying reader and then spends the bytes from the
// budget.
func (r *reader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if chunk := r.limiter.chunk(); len(p) > chunk {
		p = p[:chunk]
	}
	n, err := r.r.Read(p)
	if n > 0 {
		if werr := r.limiter.Wait(r.ctx, n); werr != nil {
			return 0, werr
		}
	}
	return n, err
}
