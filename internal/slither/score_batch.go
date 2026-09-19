package slither

import (
	"context"
	"sync"
)

const (
	// modelBatchSize caps how many files are scored in a single model call.
	modelBatchSize = 8
	// modelScoreConcurrency bounds how many scoring batches run concurrently.
	modelScoreConcurrency = 4
)

// scoreTopRows scores rows in batches using a bounded worker pool, mutating each
// row in place with model results. Batches cover disjoint index ranges, so the
// concurrent writes never overlap and output order is preserved without a lock.
// Provider-failed batches degrade only their own files to deterministic +
// model_error (handled inside ScoreBatch); caller cancellation aborts the scan.
func scoreTopRows(ctx context.Context, scorer evidenceScorer, rows []FileEvidence, batchSize, concurrency int) error {
	if scorer == nil || len(rows) == 0 {
		return ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if batchSize < 1 {
		batchSize = 1
	}
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	errCh := make(chan error, (len(rows)+batchSize-1)/batchSize)
	var wg sync.WaitGroup
	for start := 0; start < len(rows); start += batchSize {
		end := start + batchSize
		if end > len(rows) {
			end = len(rows)
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return ctx.Err()
		}
		wg.Add(1)
		// Exit condition: each goroutine scores exactly one batch, writes the
		// results back into its disjoint slice range, releases its semaphore
		// slot, and returns. No goroutine outlives wg.Wait().
		go func(batch []FileEvidence) {
			defer wg.Done()
			defer func() { <-sem }()
			scored, err := scorer.ScoreBatch(ctx, batch)
			copy(batch, scored)
			if err != nil {
				errCh <- err
			}
		}(rows[start:end])
	}
	wg.Wait()
	close(errCh)
	if err := ctx.Err(); err != nil {
		return err
	}
	for err := range errCh {
		return err
	}
	return nil
}
