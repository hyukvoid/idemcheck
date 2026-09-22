// Package integration runs the full check matrix against the SAFE and
// UNSAFE example order APIs, exactly as a user would via docker compose.
//
//	expect: safe API   -> every check passes
//	expect: unsafe API -> concurrent check fails (race detected)
package integration

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/hyukvoid/idemcheck/internal/config"
	"github.com/hyukvoid/idemcheck/internal/engine"
	"github.com/hyukvoid/idemcheck/internal/fingerprint"
	"github.com/hyukvoid/idemcheck/internal/httpx"
	"github.com/hyukvoid/idemcheck/internal/models"
	"github.com/hyukvoid/idemcheck/internal/report"
)

// startAPI builds and launches an example server on a free port, waits for
// its health endpoint, and returns the base URL plus a stop function.
func startAPI(t *testing.T, pkg string) (string, func()) {
	t.Helper()

	port := freePort(t)
	bin := filepath.Join(t.TempDir(), "api"+exeSuffix())
	build := exec.Command("go", "build", "-o", bin, pkg)
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, out)
	}

	cmd := exec.Command(bin)
	cmd.Dir = repoRoot(t)
	cmd.Env = append(os.Environ(), "PORT="+port)
	// Keep server output reachable when debugging a failed run.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", pkg, err)
	}

	base := "http://127.0.0.1:" + port
	waitHealthy(t, base+"/healthz")

	stop := func() {
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}
	return base, stop
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

func waitHealthy(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	client := &http.Client{Timeout: time.Second}
	for time.Now().Before(deadline) {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("server at %s did not become healthy", url)
}

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate test file")
	}
	return filepath.Dir(filepath.Dir(file)) // integration/ -> repo root
}

func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// runMatrix executes the same scenario set the CLI runs. Each invocation
// derives a fresh base key so server-side state from a previous run (or a
// previous check) can never mask the behavior under test.
func runMatrix(t *testing.T, baseURL string) []engine.CheckResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	runner := &engine.Runner{
		Client: httpx.NewClient(10 * time.Second),
		Spec: httpx.Spec{
			Method:    "POST",
			URL:       baseURL + "/orders",
			Body:      []byte(`{"item_id":42,"qty":1}`),
			KeyHeader: "Idempotency-Key",
			Header:    http.Header{},
		},
		FPOpts:      fingerprint.Options{},
		BaseKey:     config.NewKey(),
		Repeat:      10,
		Concurrency: 10,
	}
	results, err := runner.Run(ctx)
	if err != nil {
		t.Fatalf("run matrix: %v", err)
	}
	return results
}

func findCheck(results []engine.CheckResult, id string) engine.CheckResult {
	for _, r := range results {
		if r.ID == id {
			return r
		}
	}
	return engine.CheckResult{}
}

// Criterion 8: the safe API must pass every check, including concurrency.
func TestSafeAPIPasses(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	base, stop := startAPI(t, "./examples/safe-order-api")
	defer stop()

	results := runMatrix(t, base)
	res := reportAssemble(results)

	if res.Summary.Result != "PASS" {
		for _, c := range res.Checks {
			t.Logf("check %s = %s (%s)", c.ID, c.Status, c.Detail)
		}
		t.Fatalf("safe API must PASS, got %s (exit %d)", res.Summary.Result, res.Summary.ExitCode)
	}
	if res.Summary.ExitCode != models.ExitPass {
		t.Fatalf("exit code = %d, want 0", res.Summary.ExitCode)
	}

	conc := findCheck(results, engine.ScConcurrent)
	if conc.Unique != 1 {
		t.Fatalf("safe API produced %d unique responses under concurrency, want 1", conc.Unique)
	}
	if conc.Requests != 10 {
		t.Fatalf("concurrent requests = %d, want 10", conc.Requests)
	}
}

// Criterion 9: the unsafe API must reliably fail the concurrent check.
func TestUnsafeAPIDetectsRace(t *testing.T) {
	if testing.Short() {
		t.Skip("integration test")
	}
	base, stop := startAPI(t, "./examples/unsafe-order-api")
	defer stop()

	// Repeat a few times: a race test that only catches the bug sometimes
	// is not a useful tool. All runs must detect it.
	for i := 0; i < 3; i++ {
		t.Run(fmt.Sprintf("run%d", i+1), func(t *testing.T) {
			results := runMatrix(t, base)

			conc := findCheck(results, engine.ScConcurrent)
			if conc.Status != models.StatusFail {
				t.Fatalf("concurrent check = %s (%s), want FAIL", conc.Status, conc.Detail)
			}
			if conc.Unique < 2 {
				t.Fatalf("expected duplicate resources (unique >= 2), got %d", conc.Unique)
			}
			if len(conc.DifferingFields) == 0 || conc.DifferingFields[0] != "$.order_id" {
				t.Fatalf("expected $.order_id evidence, got %v", conc.DifferingFields)
			}

			res := reportAssemble(results)
			if res.Summary.ExitCode != models.ExitViolation {
				t.Fatalf("exit = %d, want 1 (violation)", res.Summary.ExitCode)
			}
			if len(res.Violations) == 0 {
				t.Fatal("expected at least one recorded violation")
			}
			if res.Reproduce == nil || res.Reproduce.Command == "" {
				t.Fatal("expected a reproducible re-run command")
			}

			// The demo story requires sequential retries to LOOK idempotent.
			if seq := findCheck(results, engine.ScSeq2); seq.Status != models.StatusPass {
				t.Fatalf("sequential ×2 = %s (%s): unsafe API must only fail under concurrency",
					seq.Status, seq.Detail)
			}
		})
	}
}

func reportAssemble(results []engine.CheckResult) *models.Result {
	return report.Assemble(report.AssembleInput{
		Target:  models.Target{},
		Results: results,
		Options: config.Options{},
	})
}
