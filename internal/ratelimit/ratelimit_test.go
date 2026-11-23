package ratelimit

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"
	"time"
)

// fakeClock is a clock that only moves when it is asked to.
type fakeClock struct {
	mu    sync.Mutex
	now   time.Time
	slept time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)}
}

// Now returns the current time of the fake clock.
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.now
}

// Sleep moves the fake clock forward by d and records the wait.
func (c *fakeClock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
	c.slept += d
	return nil
}

// advance moves the fake clock forward without recording a wait, which is
// what passes while the limiter is idle.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.now = c.now.Add(d)
}

// total returns the time the limiter waited.
func (c *fakeClock) total() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.slept
}

func TestNewWithoutALimit(t *testing.T) {
	t.Parallel()

	if limiter := New(0, newFakeClock()); limiter != nil {
		t.Errorf("New(0) = %v, want nil", limiter)
	}
	if limiter := New(-1, newFakeClock()); limiter != nil {
		t.Errorf("New(-1) = %v, want nil", limiter)
	}
}

func TestWaitSpendsTheBudget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rate    int64
		advance time.Duration
		waits   []int
		want    time.Duration
	}{
		{
			name:  "a full second of the budget",
			rate:  1000,
			waits: []int{1000},
			want:  time.Second,
		},
		{
			name:  "partial waits add up",
			rate:  1000,
			waits: []int{1000, 500},
			want:  1500 * time.Millisecond,
		},
		{
			name:  "one byte per second",
			rate:  1,
			waits: []int{1, 1, 1},
			want:  3 * time.Second,
		},
		{
			name:  "a fraction of a second",
			rate:  1000,
			waits: []int{100},
			want:  100 * time.Millisecond,
		},
		{
			name:    "an idle limiter accrues one second of credit",
			rate:    1000,
			advance: 10 * time.Second,
			waits:   []int{1000},
			want:    0,
		},
		{
			name:    "the credit above the idle limit is not free",
			rate:    1000,
			advance: 10 * time.Second,
			waits:   []int{1000, 1000},
			want:    time.Second,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			clock := newFakeClock()
			limiter := New(tc.rate, clock)
			if limiter == nil {
				t.Fatalf("New(%d) = nil, want a limiter", tc.rate)
			}

			clock.advance(tc.advance)
			for _, n := range tc.waits {
				if err := limiter.Wait(t.Context(), n); err != nil {
					t.Fatalf("Wait() = %v", err)
				}
			}

			if got := clock.total(); got != tc.want {
				t.Errorf("the limiter waited %v, want %v", got, tc.want)
			}
		})
	}
}

func TestReaderThrottlesTheData(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	limiter := New(1024, clock)
	content := bytes.Repeat([]byte("x"), 3*1024)

	got, err := io.ReadAll(limiter.Reader(t.Context(), bytes.NewReader(content)))
	if err != nil {
		t.Fatalf("ReadAll() = %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("ReadAll() returned %d bytes, want %d", len(got), len(content))
	}
	if want := 3 * time.Second; clock.total() != want {
		t.Errorf("the reader waited %v, want %v", clock.total(), want)
	}
}

func TestReaderDoesNotReadMoreThanOneSecondOfData(t *testing.T) {
	t.Parallel()

	limiter := New(1024, newFakeClock())
	reader := limiter.Reader(t.Context(), bytes.NewReader(make([]byte, 8*1024)))

	buf := make([]byte, 8*1024)
	n, err := reader.Read(buf)
	if err != nil {
		t.Fatalf("Read() = %v", err)
	}
	if n != 1024 {
		t.Errorf("Read() = %d bytes, want 1024", n)
	}
}

func TestReadersShareTheBudget(t *testing.T) {
	t.Parallel()

	clock := newFakeClock()
	limiter := New(1024, clock)
	content := bytes.Repeat([]byte("x"), 1024)

	for range 2 {
		if _, err := io.ReadAll(limiter.Reader(t.Context(), bytes.NewReader(content))); err != nil {
			t.Fatalf("ReadAll() = %v", err)
		}
	}

	if want := 2 * time.Second; clock.total() != want {
		t.Errorf("the readers waited %v, want %v", clock.total(), want)
	}
}

func TestLimiterIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	const (
		rate       = 1_000_000_000
		goroutines = 8
		waits      = 32
		chunk      = 1
	)

	clock := newFakeClock()
	limiter := New(rate, clock)

	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range waits {
				if err := limiter.Wait(t.Context(), chunk); err != nil {
					t.Errorf("Wait() = %v", err)
				}
			}
		})
	}
	wg.Wait()

	// Every wait is deterministic, because the fake clock only moves when
	// it is told to.
	if want := goroutines * waits * chunk * time.Second / rate; clock.total() != want {
		t.Errorf("the limiters waited %v, want %v", clock.total(), want)
	}
}

func TestChunk(t *testing.T) {
	t.Parallel()

	tests := []struct {
		rate int64
		want int
	}{
		{rate: 1, want: 1},
		{rate: 2048, want: 2048},
		{rate: maxRead, want: maxRead},
		{rate: 1 << 40, want: maxRead},
	}
	for _, tc := range tests {
		limiter := New(tc.rate, newFakeClock())
		if got := limiter.chunk(); got != tc.want {
			t.Errorf("chunk() for %d bytes/s = %d, want %d", tc.rate, got, tc.want)
		}
	}
}

func TestRealClock(t *testing.T) {
	t.Parallel()

	clock := RealClock{}
	start := clock.Now()
	if err := clock.Sleep(t.Context(), time.Millisecond); err != nil {
		t.Fatalf("Sleep() = %v", err)
	}
	if clock.Now().Before(start) {
		t.Error("the real clock went backwards")
	}
}

func TestWaitStopsWhenTheContextIsDone(t *testing.T) {
	t.Parallel()

	limiter := New(1, newFakeClock())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := limiter.Wait(ctx, 10); !errors.Is(err, context.Canceled) {
		t.Errorf("Wait() = %v, want %v", err, context.Canceled)
	}
}

func TestReaderStopsWhenTheContextIsDone(t *testing.T) {
	t.Parallel()

	limiter := New(1024, newFakeClock())
	ctx, cancel := context.WithCancel(t.Context())
	reader := limiter.Reader(ctx, bytes.NewReader(make([]byte, 4*1024)))

	if _, err := reader.Read(make([]byte, 512)); err != nil {
		t.Fatalf("Read() = %v", err)
	}

	cancel()
	if _, err := reader.Read(make([]byte, 512)); !errors.Is(err, context.Canceled) {
		t.Errorf("Read() = %v, want %v", err, context.Canceled)
	}
}
