package engine

import (
	"context"
	"sync"
	"time"

	"github.com/hyukvoid/idemcheck/internal/fingerprint"
	"github.com/hyukvoid/idemcheck/internal/httpx"
	"github.com/hyukvoid/idemcheck/internal/models"
)

// Concurrent sends n equivalent requests as simultaneously as possible,
// all sharing one idempotency key.
//
// Barrier protocol:
//
//  1. spawn n workers on the same spec
//  2. each worker signals READY and parks on the start channel
//  3. main waits until all n workers are READY
//  4. close(start) releases everyone at the same instant
//  5. workers fire their requests; results land in out[worker]
//
// Workers write only to their own pre-allocated slots, so collection needs
// no locks and `go test -race` stays clean. The `release` timestamp is
// published through the channel close, which gives every worker a
// happens-before edge for reading it.
func Concurrent(ctx context.Context, client *httpx.Client, spec httpx.Spec, key string, n int, fpOpts fingerprint.Options) (*CheckResult, error) {
	runStart := time.Now()
	ready := make(chan struct{}, n) // buffered: workers never block signaling
	start := make(chan struct{})
	out := make([]httpx.Outcome, n)
	offsets := make([]time.Duration, n)
	var release time.Time // written before close(start); read only after <-start

	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ready <- struct{}{}
			select {
			case <-start:
			case <-ctx.Done():
				out[id] = httpx.Outcome{Worker: id, Err: ctx.Err()}
				return
			}
			offsets[id] = time.Since(release)
			out[id] = client.Do(ctx, spec, key, id)
		}(i)
	}

	// Hold the gate until every worker is parked and ready to fire.
	for i := 0; i < n; i++ {
		select {
		case <-ready:
		case <-ctx.Done():
			close(start) // release parked workers so they can exit
			wg.Wait()
			return nil, ctx.Err()
		}
	}

	release = time.Now()
	close(start)
	wg.Wait()

	res, err := collect(out, fpOpts)
	if err != nil {
		return nil, err
	}
	res.Duration = time.Since(runStart)
	res.Timings = buildTimings(out, offsets)
	analyzeEvidence(res)
	return res, nil
}

// buildTimings renders per-request timing records. StartOffset is how long
// after the barrier release the worker began its request — the number that
// proves whether the burst was actually simultaneous.
func buildTimings(out []httpx.Outcome, offsets []time.Duration) []models.RequestTiming {
	timings := make([]models.RequestTiming, 0, len(out))
	for i, oc := range out {
		if oc.Err != nil && oc.Latency == 0 {
			continue // worker aborted before sending; nothing observable
		}
		timings = append(timings, models.RequestTiming{
			Worker:      i,
			StartOffset: offsets[i].Milliseconds(),
			Latency:     oc.Latency.Milliseconds(),
		})
	}
	return timings
}
