// Package workpool runs work items with bounded concurrency.
package workpool

import (
	"context"
	"sync"
)

// Run calls work for every item, with at most n calls in flight. report is
// called once for every finished item, on the goroutine that called Run, so it
// may print or count without locking. After ctx is cancelled no new item is
// started; Run waits for the running ones and returns the number of items
// that were never started.
func Run[T any](ctx context.Context, n int, items []T, work func(context.Context, T) error, report func(T, error)) (notStarted int) {
	if n < 1 {
		n = 1
	}
	type result struct {
		i   int
		err error
	}
	jobs := make(chan int)
	results := make(chan result)
	var wg sync.WaitGroup
	for range min(n, len(items)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range jobs {
				results <- result{i, work(ctx, items[i])}
			}
		}()
	}
	started := 0
	go func() {
		defer close(jobs)
		for i := range items {
			if ctx.Err() != nil {
				return
			}
			select {
			case jobs <- i:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		wg.Wait()
		close(results)
	}()
	for r := range results {
		started++
		report(items[r.i], r.err)
	}
	return len(items) - started
}
