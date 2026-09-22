package engine

import (
	"context"
	"time"

	"github.com/idemcheck/idemcheck/internal/fingerprint"
	"github.com/idemcheck/idemcheck/internal/httpx"
)

// Sequential sends n requests one after another with the same key.
// Ordering matters: the server must answer request k before k+1 starts.
func Sequential(ctx context.Context, client *httpx.Client, spec httpx.Spec, key string, n int, fpOpts fingerprint.Options) (*CheckResult, error) {
	start := time.Now()
	outcomes := make([]httpx.Outcome, 0, n)
	for i := 0; i < n; i++ {
		if ctx.Err() != nil {
			break
		}
		outcomes = append(outcomes, client.Do(ctx, spec, key, i))
	}
	res, err := collect(outcomes, fpOpts)
	if err != nil {
		return nil, err
	}
	res.Duration = time.Since(start)
	analyzeEvidence(res)
	return res, nil
}
