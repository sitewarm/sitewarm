package verify

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/sitewarm/sitewarm/internal/cdn"
	"github.com/sitewarm/sitewarm/internal/probe"
)

// The retry window is inclusive: a retry that lands exactly --max-wait after
// the first attempt is still sent, and is not dropped by the check when it is
// scheduled or by the one when it comes due.
func TestSchedulerRetryExactlyAtMaxWaitIsSent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := defaults
		cfg.Concurrency = 1
		cfg.RPS = 0
		cfg.RetryInterval = 10 * time.Second
		cfg.MaxWait = 10 * time.Second
		out, stopped := Run(context.Background(), cfg, pairs(1), func(ctx context.Context, p Pair) probe.Observation {
			return respond("MISS")
		})
		if stopped != nil {
			t.Fatalf("stopped = %v", stopped)
		}
		if o := out[0]; o.State != cdn.Miss || o.Attempts != 2 {
			t.Fatalf("state %s after %d attempts, want miss after 2 (first + the retry at exactly max-wait)", o.State, o.Attempts)
		}
	})
}

// A retry one tick past the window is not sent.
func TestSchedulerRetryPastMaxWaitIsDropped(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := defaults
		cfg.Concurrency = 1
		cfg.RPS = 0
		cfg.RetryInterval = 10*time.Second + time.Nanosecond
		cfg.MaxWait = 10 * time.Second
		out, _ := Run(context.Background(), cfg, pairs(1), func(ctx context.Context, p Pair) probe.Observation {
			return respond("MISS")
		})
		if o := out[0]; o.State != cdn.Miss || o.Attempts != 1 {
			t.Fatalf("state %s after %d attempts, want miss after 1", o.State, o.Attempts)
		}
	})
}

// Retries that become due together go out in the order they were scheduled.
func TestSchedulerEqualDueRetriesKeepSchedulingOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := defaults
		cfg.Concurrency = 1
		cfg.RPS = 0
		cfg.RetryInterval = 10 * time.Second
		cfg.MaxWait = 15 * time.Second
		const n = 6
		var order []string
		Run(context.Background(), cfg, pairs(n), func(ctx context.Context, p Pair) probe.Observation {
			order = append(order, p.URL)
			return respond("MISS")
		})
		if len(order) != 2*n {
			t.Fatalf("%d requests, want %d (every pair twice)", len(order), 2*n)
		}
		for i, p := range pairs(n) {
			if order[i] != p.URL || order[n+i] != p.URL {
				t.Fatalf("request order %v: want the pairs in order, then the retries in the same order", order)
			}
		}
	})
}

// With no rate limit the scheduler still never has more requests in flight
// than workers, and a run longer than the result buffer does not stall.
func TestSchedulerUnlimitedRateRespectsConcurrency(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := defaults
		cfg.Concurrency = 2
		cfg.RPS = 0
		var mu sync.Mutex
		running, peak := 0, 0
		out, stopped := Run(context.Background(), cfg, pairs(50), func(ctx context.Context, p Pair) probe.Observation {
			mu.Lock()
			running++
			peak = max(peak, running)
			mu.Unlock()
			time.Sleep(time.Millisecond)
			mu.Lock()
			running--
			mu.Unlock()
			return respond("HIT")
		})
		if stopped != nil {
			t.Fatalf("stopped = %v", stopped)
		}
		for _, o := range out {
			if o.State != cdn.Hit || o.Attempts != 1 {
				t.Fatalf("%s: %s after %d attempts", o.URL, o.State, o.Attempts)
			}
		}
		if peak != cfg.Concurrency {
			t.Fatalf("peak %d requests in flight, want exactly %d", peak, cfg.Concurrency)
		}
	})
}

// When the run times out, no pair beyond the ones already in flight is handed
// to a worker: the requests that never started stay incomplete.
func TestSchedulerNoRequestStartsBeyondConcurrencyBeforeTimeout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		cfg := defaults
		cfg.Concurrency = 2
		cfg.RPS = 0
		cfg.RunTimeout = 10 * time.Second
		var mu sync.Mutex
		calls := 0
		out, stopped := Run(context.Background(), cfg, pairs(10), func(ctx context.Context, p Pair) probe.Observation {
			mu.Lock()
			calls++
			mu.Unlock()
			<-ctx.Done()
			return probe.Observation{Start: time.Now(), Err: errors.New("canceled")}
		})
		if !errors.Is(stopped, context.DeadlineExceeded) {
			t.Fatalf("stopped = %v, want the run timeout", stopped)
		}
		if calls != cfg.Concurrency {
			t.Fatalf("%d requests started, want %d (one per worker)", calls, cfg.Concurrency)
		}
		started := 0
		for _, o := range out {
			if o.State != cdn.Incomplete {
				t.Fatalf("%s = %s, want incomplete", o.URL, o.State)
			}
			if o.First != nil {
				t.Fatalf("%s has an observation from after the timeout", o.URL)
			}
			started += o.Attempts
		}
		if started != 0 {
			t.Fatalf("%d attempts counted from canceled requests", started)
		}
	})
}

// A slot that opens exactly now needs no wait, and a canceled context is then
// reported, every time, not left to a race with a zero-length timer.
func TestLimiterWaitWithSlotExactlyNowReportsCancel(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		canceled, cancel := context.WithCancel(context.Background())
		cancel()
		for i := range 200 {
			l := NewLimiter(1) // fresh: the first slot is now
			if err := l.Wait(canceled); !errors.Is(err, context.Canceled) {
				t.Fatalf("attempt %d: Wait = %v, want context canceled", i, err)
			}
			if err := NewLimiter(1).Wait(context.Background()); err != nil {
				t.Fatalf("attempt %d: Wait = %v on a free slot", i, err)
			}
		}
	})
}

func TestNewLimiterInterval(t *testing.T) {
	for _, tc := range []struct {
		rps  float64
		want time.Duration
	}{
		{4, 250 * time.Millisecond},
		{0.5, 2 * time.Second},
		{1, time.Second},
		{0, 0},
		{-3, 0},
	} {
		if got := NewLimiter(tc.rps).interval; got != tc.want {
			t.Errorf("NewLimiter(%v).interval = %v, want %v", tc.rps, got, tc.want)
		}
	}
}
