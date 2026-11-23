// Package ratelimit provides a token bucket and a throttled reader.
//
// The limiter is hand-rolled instead of using golang.org/x/time/rate so that
// tong keeps its dependency list to the S3 SDK and the command-line parser,
// and so that the waits are driven by an injected clock and are therefore
// deterministic in tests.
package ratelimit

import (
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
	// Sleep waits for d to pass.
	Sleep(d time.Duration)
}

// RealClock is the clock of the running process.
type RealClock struct{}

// Now returns the current time.
func (RealClock) Now() time.Time { return time.Now() }

// Sleep waits for d to pass.
func (RealClock) Sleep(d time.Duration) { time.Sleep(d) }

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

// Wait spends n bytes from the budget, blocking until they are available.
func (l *Limiter) Wait(n int) {
	if n <= 0 {
		return
	}

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
		return
	}

	// The budget is in debt by the missing bytes. The next call sees a
	// virtual time that is already that far ahead, so that the bytes of a
	// single read are spread over the budget instead of arriving as one
	// second's worth of data at a time.
	missing := need - l.tokens
	l.tokens = 0
	wait := max(time.Duration(missing/l.rate*float64(time.Second)), 0)
	l.last = now.Add(wait)
	l.clock.Sleep(wait)
}

// Reader wraps r so that every read spends its bytes from the budget, which
// pushes the backpressure up to the sender.
func (l *Limiter) Reader(r io.Reader) io.Reader {
	return &reader{limiter: l, r: r}
}

// chunk returns the largest read the limiter lets through at once: one
// second of the budget, so that a slow budget blocks for at most a second
// at a time.
func (l *Limiter) chunk() int {
	return int(min(max(l.rate, 1), maxRead))
}

// reader is a throttled reader.
type reader struct {
	limiter *Limiter
	r       io.Reader
}

// Read reads from the underlying reader and then spends the bytes from the
// budget.
func (r *reader) Read(p []byte) (int, error) {
	if chunk := r.limiter.chunk(); len(p) > chunk {
		p = p[:chunk]
	}
	n, err := r.r.Read(p)
	if n > 0 {
		r.limiter.Wait(n)
	}
	return n, err
}
