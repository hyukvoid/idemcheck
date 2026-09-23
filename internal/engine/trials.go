package engine

import (
	"fmt"
	"time"

	"github.com/hyukvoid/idemcheck/internal/models"
)

// statusRank orders statuses for evidence-source selection when aggregating
// trials: the most decisive trial supplies the reported evidence.
func statusRank(s models.CheckStatus) int {
	switch s {
	case models.StatusFail:
		return 3
	case models.StatusError:
		return 2
	case models.StatusInconclusive:
		return 1
	default:
		return 0
	}
}

// aggregateTrials folds per-trial results into one reportable check.
// Evidence (groups, differing fields, timings) comes from the first trial
// with the most decisive status; counts are summed across trials.
func aggregateTrials(trials []CheckResult, conc int, settle time.Duration) *CheckResult {
	agg := &CheckResult{
		ID:             ScConcurrent,
		Name:           fmt.Sprintf("Concurrent retry ×%d", conc),
		EvidenceFields: map[string]map[string]any{},
	}
	if len(trials) == 0 {
		agg.Status = models.StatusError
		agg.Detail = "no trials completed"
		return agg
	}

	var failCount, errCount, incCount, confirmed, ranReplay int
	srcIdx := 0
	for i := range trials {
		t := &trials[i]
		agg.Requests += t.Requests
		agg.Observed += t.Observed
		agg.Transport += t.Transport
		agg.Oversize += t.Oversize
		agg.Duration += t.Duration
		if statusRank(t.Status) > statusRank(trials[srcIdx].Status) {
			srcIdx = i
		}
		switch t.Status {
		case models.StatusFail:
			failCount++
		case models.StatusError:
			errCount++
		case models.StatusInconclusive:
			incCount++
		}
		if t.Replay != nil {
			ranReplay++
			if t.Replay.Confirmed {
				confirmed++
			}
		}
	}
	agg.Trials = len(trials)

	// Evidence and per-burst timing come from the decisive trial.
	src := &trials[srcIdx]
	agg.Status = src.Status
	agg.Unique = src.Unique
	agg.Logical = src.Logical
	agg.Groups = src.Groups
	agg.DifferingFields = src.DifferingFields
	agg.EvidenceFields = src.EvidenceFields
	agg.Timings = src.Timings
	agg.BaselineStatus = src.BaselineStatus
	agg.FirstError = src.FirstError

	n := len(trials)
	switch agg.Status {
	case models.StatusFail:
		agg.Detail = fmt.Sprintf("Concurrency race detected in %d / %d trials", failCount, n)
	case models.StatusError:
		agg.Detail = fmt.Sprintf("execution failed in %d / %d trials: %s", errCount, n, src.Detail)
	case models.StatusInconclusive:
		agg.Detail = fmt.Sprintf("no race verdict in %d / %d trials: %s", incCount, n, src.Detail)
	default:
		agg.Detail = fmt.Sprintf("No observable race in %d trials (absence of an observed race is not proof of idempotency)", n)
	}

	// Aggregate phase lines (the per-trial ones belonged to one burst).
	agg.ActionPhases = []string{
		fmt.Sprintf("BURST: %d requests × %d trials (isolated per-trial keys)", conc, n),
	}
	if settle > 0 {
		agg.ActionPhases = append(agg.ActionPhases,
			fmt.Sprintf("SETTLE: %s × %d trials", settle, n))
	}
	switch {
	case ranReplay == 0:
		agg.ActionPhases = append(agg.ActionPhases, "REPLAY: not run (rejected baseline)")
	case confirmed == ranReplay:
		agg.ActionPhases = append(agg.ActionPhases,
			fmt.Sprintf("REPLAY: %d/%d confirmed a logical result", confirmed, ranReplay))
	default:
		agg.ActionPhases = append(agg.ActionPhases,
			fmt.Sprintf("REPLAY: %d/%d confirmed a logical result, %d inconclusive", confirmed, ranReplay, ranReplay-confirmed))
	}
	return agg
}
