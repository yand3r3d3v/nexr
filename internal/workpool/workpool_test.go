package workpool

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunBoundsConcurrency(t *testing.T) {
	items := make([]int, 50)
	for i := range items {
		items[i] = i
	}
	var inFlight, peak atomic.Int32
	seen := map[int]bool{}
	failed := 0
	left := Run(context.Background(), 4, items, func(_ context.Context, i int) error {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(time.Millisecond)
		inFlight.Add(-1)
		if i%10 == 0 {
			return errors.New("boom")
		}
		return nil
	}, func(i int, err error) {
		seen[i] = true // no lock: report runs on the caller's goroutine
		if err != nil {
			failed++
		}
	})
	if left != 0 || len(seen) != 50 || failed != 5 {
		t.Fatalf("left %d, seen %d, failed %d", left, len(seen), failed)
	}
	if p := peak.Load(); p > 4 || p < 2 {
		t.Fatalf("peak concurrency %d", p)
	}
}

func TestRunStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	items := make([]int, 100)
	reported := 0
	left := Run(ctx, 2, items, func(ctx context.Context, _ int) error {
		cancel()
		return ctx.Err()
	}, func(int, error) { reported++ })
	if left == 0 || reported+left != 100 || reported > 4 {
		t.Fatalf("reported %d, not started %d", reported, left)
	}
}

func TestRunEmptyAndSerial(t *testing.T) {
	if left := Run(context.Background(), 0, []int(nil), func(context.Context, int) error { return nil }, func(int, error) {}); left != 0 {
		t.Fatal(left)
	}
	var order []int
	Run(context.Background(), 0, []int{1, 2, 3}, func(context.Context, int) error { return nil }, func(i int, _ error) { order = append(order, i) })
	if len(order) != 3 || order[0] != 1 || order[2] != 3 {
		t.Fatalf("serial order %v", order)
	}
}
