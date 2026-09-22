// Package engine executes idempotency scenarios: sequential retries and
// barrier-synchronized concurrent duplicate bursts.
package engine

import (
	"sort"
	"time"

	"github.com/idemcheck/idemcheck/internal/fingerprint"
	"github.com/idemcheck/idemcheck/internal/httpx"
	"github.com/idemcheck/idemcheck/internal/models"
)

// Group aggregates responses sharing one semantic fingerprint.
type Group struct {
	Fingerprint fingerprint.Fingerprint
	Count       int
}

// CheckResult is the outcome of one scenario.
type CheckResult struct {
	ID     string
	Name   string
	Status models.CheckStatus
	// Requests is how many requests were attempted.
	Requests int
	// Observed is how many completed with a response.
	Observed int
	// BaselineStatus is the status of the first observed response.
	BaselineStatus int
	// Unique is how many distinct semantic responses were observed.
	Unique   int
	Detail   string
	Duration time.Duration
	// Groups is sorted: highest count first, fingerprint as tie-break.
	Groups  []Group
	Timings []models.RequestTiming
	// Err carries transport/execution failures (exit code 2 territory).
	Err error
	// FirstError describes the first transport error, for display.
	FirstError string
	// DifferingFields are JSON paths that vary between groups.
	DifferingFields []string
	// EvidenceFields maps fingerprint -> JSON path -> observed value.
	EvidenceFields map[string]map[string]any
}

// collect fingerprints raw outcomes and groups them. Transport errors are
// recorded but never mixed into the fingerprint set.
func collect(outcomes []httpx.Outcome, fpOpts fingerprint.Options) (*CheckResult, error) {
	res := &CheckResult{EvidenceFields: map[string]map[string]any{}}
	byFP := map[string]*Group{}
	order := []string{} // first-seen order keeps grouping deterministic

	for _, oc := range outcomes {
		res.Requests++
		if oc.Err != nil {
			if res.FirstError == "" {
				res.FirstError = httpx.Describe(oc.Err)
			}
			continue
		}
		res.Observed++
		if res.BaselineStatus == 0 {
			res.BaselineStatus = oc.StatusCode
		}
		fp, err := fingerprint.Compute(&fingerprint.Response{
			StatusCode: oc.StatusCode,
			Header:     oc.Header,
			Body:       oc.Body,
		}, fpOpts)
		if err != nil {
			return nil, err
		}
		g, ok := byFP[fp.Value]
		if !ok {
			g = &Group{Fingerprint: fp}
			byFP[fp.Value] = g
			order = append(order, fp.Value)
		}
		g.Count++
	}

	res.Unique = len(byFP)
	for _, v := range order {
		res.Groups = append(res.Groups, *byFP[v])
	}
	// Highest count first; equal counts keep first-seen order (stable sort),
	// so evidence reads in the order the responses actually arrived.
	sort.SliceStable(res.Groups, func(i, j int) bool {
		return res.Groups[i].Count > res.Groups[j].Count
	})
	return res, nil
}

// analyzeEvidence computes differing fields across groups plus each group's
// observed value for those fields. Raw (non-JSON) bodies get no field-level
// evidence — fingerprint differences alone remain authoritative.
func analyzeEvidence(res *CheckResult) {
	if res.Unique < 2 {
		return
	}
	base := res.Groups[0].Fingerprint.Body
	if !base.IsJSON {
		return
	}
	fieldSet := map[string]struct{}{}
	for i := 1; i < len(res.Groups); i++ {
		other := res.Groups[i].Fingerprint.Body
		if !other.IsJSON {
			continue
		}
		for _, p := range fingerprint.DiffPaths(base.Tree, other.Tree) {
			fieldSet[p] = struct{}{}
		}
	}
	if len(fieldSet) == 0 {
		return
	}
	fields := make([]string, 0, len(fieldSet))
	for p := range fieldSet {
		fields = append(fields, p)
	}
	sort.Strings(fields)
	res.DifferingFields = fields

	for _, g := range res.Groups {
		if !g.Fingerprint.Body.IsJSON {
			continue
		}
		observed := map[string]any{}
		for _, p := range fields {
			if v, ok := fingerprint.ValueAt(g.Fingerprint.Body.Tree, p); ok {
				observed[p] = v
			}
		}
		res.EvidenceFields[g.Fingerprint.Value] = observed
	}
}

// Evidence renders this result as report-model evidence groups.
// labels maps fingerprint -> "A"/"B"/... assigned by the caller for display.
func (r *CheckResult) Evidence(labels map[string]string) []models.EvidenceGroup {
	out := make([]models.EvidenceGroup, 0, len(r.Groups))
	for _, g := range r.Groups {
		label := labels[g.Fingerprint.Value]
		if label == "" {
			label = "?"
		}
		fields := map[string]any{}
		for k, v := range r.EvidenceFields[g.Fingerprint.Value] {
			fields[k] = v
		}
		if len(fields) == 0 && g.Fingerprint.Body.IsJSON && r.Unique == 1 {
			// Single-group checks still show status; body omitted on purpose
			// (it may contain sensitive data — fingerprint is enough).
		}
		out = append(out, models.EvidenceGroup{
			CheckID:     r.ID,
			Label:       label,
			Fingerprint: g.Fingerprint.Value,
			Count:       g.Count,
			Status:      g.Fingerprint.Status,
			Fields:      fields,
		})
	}
	return out
}

// ToModel converts a CheckResult into the report model.
func (r *CheckResult) ToModel() models.Check {
	return models.Check{
		ID:              r.ID,
		Name:            r.Name,
		Status:          r.Status,
		Requests:        r.Requests,
		UniqueResponses: r.Unique,
		Detail:          r.Detail,
		DurationMS:      r.Duration.Milliseconds(),
		Timings:         r.Timings,
	}
}
