package engine

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/hyukvoid/idemcheck/internal/config"
	"github.com/hyukvoid/idemcheck/internal/fingerprint"
	"github.com/hyukvoid/idemcheck/internal/httpx"
	"github.com/hyukvoid/idemcheck/internal/models"
)

// Replay retry budget: fixed interval, bounded attempts and wall time.
// Transient answers are worth retrying (the burst itself may have tripped a
// rate limit); unknown (non-policy) statuses are final — a retry cannot
// change a verdict that is already inconclusive.
const (
	replayInterval    = 200 * time.Millisecond
	replayMaxAttempts = 10
)

// ReplayInfo describes a check's REPLAY phase: the final answer after the
// retry budget, used by the evaluator to decide whether transient responses
// were confirmed away.
type ReplayInfo struct {
	Attempts  int
	Status    int    // final HTTP status (0 = no response)
	Confirmed bool   // final answer was a 2xx logical result
	Exhausted bool   // budget spent while still answering transient
	Oversize  bool   // final body exceeded the read limit
	FinalErr  string // transport failure text when no response arrived
}

// burstPhase renders the BURST phase line: proof the requests were released
// together (spread = max start offset after the barrier).
func burstPhase(n int, timings []models.RequestTiming) string {
	var spread int64
	for _, t := range timings {
		if t.StartOffset > spread {
			spread = t.StartOffset
		}
	}
	return fmt.Sprintf("BURST: %d requests released together (spread %d ms)", n, spread)
}

// hasTransientGroup reports whether the check already observed a
// policy-acceptable transient response (the condition under which a
// sequential check replays).
func hasTransientGroup(res *CheckResult, pol config.Policy) bool {
	for _, g := range res.Groups {
		st := g.Fingerprint.Status
		isSuccess := st >= 200 && st < 300
		if !isSuccess && pol.IsTransient(st) {
			return true
		}
	}
	return false
}

// finalizeSameKey runs SETTLE+REPLAY when it can still change the verdict,
// then evaluates. Replay runs when alwaysReplay (the concurrent check:
// confirm the stored logical result after the burst settles) or when
// transients are present (sequential). A rejected baseline makes the extra
// request pointless.
func (r *Runner) finalizeSameKey(ctx context.Context, res *CheckResult, spec httpx.Spec, key string, pol config.Policy, alwaysReplay bool) error {
	baselineRejected := res.Observed > 0 && res.BaselineStatus >= 400
	var rp *ReplayInfo
	if !baselineRejected && (alwaysReplay || hasTransientGroup(res, pol)) {
		info, err := r.settleAndReplay(ctx, res, spec, key, pol)
		if err != nil {
			return err
		}
		rp = info
	}
	evaluateSameKey(res, pol, rp)
	return nil
}

// settleAndReplay waits out in-flight server work (SETTLE), then replays the
// same request with the same key until it gets a classifiable answer or the
// retry budget runs out (REPLAY). The final outcome is merged into res for
// verdict and evidence.
func (r *Runner) settleAndReplay(ctx context.Context, res *CheckResult, spec httpx.Spec, key string, pol config.Policy) (*ReplayInfo, error) {
	if r.Settle > 0 {
		select {
		case <-time.After(r.Settle):
			res.ActionPhases = append(res.ActionPhases, fmt.Sprintf("SETTLE: %s before replay", r.Settle))
		case <-ctx.Done():
			// Aborted: the replay below fails fast and the verdict stays
			// honest (inconclusive).
		}
	}

	info := &ReplayInfo{}
	deadline := time.Now().Add(r.ReplayTimeout)
	for {
		info.Attempts++
		oc := r.Client.Do(ctx, spec, key, r.Concurrency)

		// Oversize and non-transient answers are final; transport failures
		// and transients are worth another attempt within budget.
		final := oc.Oversize || (oc.Err == nil && !pol.IsTransient(oc.StatusCode))
		if !final && (info.Attempts >= replayMaxAttempts || !time.Now().Before(deadline) || ctx.Err() != nil) {
			final = true
		}
		if !final {
			select {
			case <-time.After(replayInterval):
			case <-ctx.Done():
			}
			continue
		}

		info.Status = oc.StatusCode
		switch {
		case oc.Oversize:
			info.Oversize = true
		case oc.Err != nil:
			info.FinalErr = httpx.Describe(oc.Err)
		case oc.StatusCode >= 200 && oc.StatusCode < 300:
			info.Confirmed = true
		default:
			info.Exhausted = pol.IsTransient(oc.StatusCode)
		}
		if err := mergeOutcome(res, oc, r.FPOpts); err != nil {
			return info, err
		}
		analyzeEvidence(res)
		break
	}

	res.Replay = info
	res.ActionPhases = append(res.ActionPhases, replayPhase(info))
	return info, nil
}

// replayPhase renders the REPLAY phase line for one check.
func replayPhase(info *ReplayInfo) string {
	switch {
	case info.Oversize:
		return fmt.Sprintf("REPLAY: %d request(s) -> body exceeded the read limit", info.Attempts)
	case info.FinalErr != "":
		return fmt.Sprintf("REPLAY: %d request(s) -> no response: %s", info.Attempts, info.FinalErr)
	case info.Confirmed && info.Attempts == 1:
		return fmt.Sprintf("REPLAY: 1 request -> HTTP %d (confirmed a logical result)", info.Status)
	case info.Confirmed:
		return fmt.Sprintf("REPLAY: %d requests -> HTTP %d (after transient retries)", info.Attempts, info.Status)
	case info.Exhausted:
		return fmt.Sprintf("REPLAY: %d requests -> HTTP %d (transient retry budget exhausted)", info.Attempts, info.Status)
	default:
		return fmt.Sprintf("REPLAY: %d requests -> HTTP %d", info.Attempts, info.Status)
	}
}

// mergeOutcome folds one extra outcome (the replay) into an already
// collected result, rebuilding the fingerprint grouping.
func mergeOutcome(res *CheckResult, oc httpx.Outcome, fpOpts fingerprint.Options) error {
	res.Requests++
	switch {
	case oc.Oversize:
		res.Observed++
		res.Oversize++
	case oc.Err != nil:
		res.Transport++
		if res.FirstError == "" {
			res.FirstError = httpx.Describe(oc.Err)
		}
	default:
		res.Observed++
		fp, err := fingerprint.Compute(toResponse(oc), fpOpts)
		if err != nil {
			return err
		}
		for i := range res.Groups {
			if res.Groups[i].Fingerprint.Value == fp.Value {
				res.Groups[i].Count++
				sortGroups(res.Groups)
				return nil
			}
		}
		res.Groups = append(res.Groups, Group{Fingerprint: fp, Count: 1})
		res.Unique = len(res.Groups)
		sortGroups(res.Groups)
	}
	return nil
}

// sortGroups orders evidence: highest count first, stable first-seen order
// for ties (same rule as collection).
func sortGroups(groups []Group) {
	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].Count > groups[j].Count
	})
}
