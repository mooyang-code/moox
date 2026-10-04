package pyexec

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/pyruntime/process"
)

func TestExecutorRunsBatch(t *testing.T) {
	executor := newTestExecutor(t, 1, 3*time.Second)
	frame := testFrame()
	path := factorPath(t, "simple.py")
	req := Request{
		Frame:   frame,
		Context: testContext("factor_a", "factor_b"),
		Factors: []FactorCall{
			testFactor(t, "factor_a", path, `{"scale":2}`),
			testFactor(t, "factor_b", path, `{"scale":3}`),
		},
	}
	results, err := executor.Exec(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}
	if results[0].FactorID != "factor_a" || results[1].FactorID != "factor_b" {
		t.Fatalf("unexpected result identities: %+v", results)
	}
	for _, result := range results {
		if result.Err != nil {
			t.Fatalf("factor %s failed: %v", result.FactorID, result.Err)
		}
		if len(result.Columns) != 3 || result.Columns[2] != "value_out" || len(result.Rows) != 1 {
			t.Fatalf("unexpected result shape: %+v", result)
		}
	}
	if got := results[0].Rows[0][2]; got != float64(6) {
		t.Fatalf("factor_a value = %v, want 6", got)
	}
	if got := results[1].Rows[0][2]; got != float64(9) {
		t.Fatalf("factor_b value = %v, want 9", got)
	}
}

func TestExecutorTimeoutReplacesWorker(t *testing.T) {
	executor := newTestExecutor(t, 1, 3*time.Second)
	hang := Request{
		Frame: testFrame(), Context: testContext("hang"),
		Factors: []FactorCall{testFactor(t, "hang", factorPath(t, "hang.py"), `{}`)},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := executor.Exec(ctx, hang); err == nil {
		t.Fatal("Exec succeeded despite deadline")
	}
	if got := executor.Busy(); got != 0 {
		t.Fatalf("Busy after timeout = %d, want 0", got)
	}
	good := Request{
		Frame: testFrame(), Context: testContext("good"),
		Factors: []FactorCall{testFactor(t, "good", factorPath(t, "simple.py"), `{}`)},
	}
	results, err := executor.Exec(context.Background(), good)
	if err != nil {
		t.Fatalf("replacement worker request failed: %v", err)
	}
	if len(results) != 1 || results[0].Err != nil {
		t.Fatalf("replacement worker returned %+v", results)
	}
}

func TestExecutorCrashReturnsCallError(t *testing.T) {
	executor := newTestExecutor(t, 1, 3*time.Second)
	req := Request{
		Frame: testFrame(), Context: testContext("crash"),
		Factors: []FactorCall{testFactor(t, "crash", factorPath(t, "crash.py"), `{}`)},
	}
	if _, err := executor.Exec(context.Background(), req); err == nil {
		t.Fatal("Exec succeeded after the Python worker exited")
	}
	if got := executor.Busy(); got != 0 {
		t.Fatalf("Busy after crash = %d, want 0", got)
	}
}

func TestExecutorLimitsConcurrencyToWorkers(t *testing.T) {
	executor := newTestExecutor(t, 2, 3*time.Second)
	counterPath := filepath.Join(t.TempDir(), "counter.json")
	if err := os.WriteFile(counterPath, []byte(`{"active":0,"maximum":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	path := factorPath(t, "concurrency.py")
	request := func(id string) Request {
		factor := testFactor(t, id, path, mustJSON(t, map[string]string{"counter_path": counterPath}))
		return Request{Frame: testFrame(), Context: testContext(id), Factors: []FactorCall{factor}}
	}
	var wg sync.WaitGroup
	errCh := make(chan error, 4)
	for i := range 4 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			result, err := executor.Exec(context.Background(), request("concurrent_"+string(rune('a'+i))))
			if err != nil {
				errCh <- err
				return
			}
			if len(result) != 1 || result[0].Err != nil {
				errCh <- errUnexpectedResult(result)
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
	if got := executor.Busy(); got != 0 {
		t.Fatalf("Busy after concurrent calls = %d, want 0", got)
	}
	raw, err := os.ReadFile(counterPath)
	if err != nil {
		t.Fatal(err)
	}
	var state struct {
		Active  int `json:"active"`
		Maximum int `json:"maximum"`
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if state.Active != 0 || state.Maximum != 2 {
		t.Fatalf("counter state = %+v, want active=0 maximum=2", state)
	}
}

func newTestExecutor(t *testing.T, workers int, timeout time.Duration) *Pool {
	t.Helper()
	python, err := filepath.Abs("../../pyworker/worker.py")
	if err != nil {
		t.Fatal(err)
	}
	factorsDir, err := filepath.Abs("testdata")
	if err != nil {
		t.Fatal(err)
	}
	executor, err := New(context.Background(), workers, process.Config{
		PythonBin: "python3", WorkerPath: python, Args: []string{"--factors-dir", factorsDir}, TaskTimeout: timeout,
	})
	if err != nil {
		t.Fatalf("create executor: %v", err)
	}
	t.Cleanup(func() {
		if err := executor.Close(); err != nil {
			t.Errorf("close executor: %v", err)
		}
	})
	return executor
}

func factorPath(t *testing.T, name string) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func testFactor(t *testing.T, id, path, params string) FactorCall {
	t.Helper()
	source, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(source)
	return FactorCall{
		FactorID: id, Name: filepath.Base(path), SourceHash: "sha256:" + hex.EncodeToString(hash[:]),
		SourcePath: path, FactorType: "timeseries", InputColumns: []string{"value"},
		Outputs: []string{"value_out"}, Params: json.RawMessage(params), LookbackPeriods: 2,
	}
}

func testFrame() map[string]any {
	return map[string]any{
		"columns": []string{"data_time", "series_tag", "value"},
		"rows": [][]any{
			{"2026-07-27T23:59:00Z", "venue:binance", 2.0},
			{"2026-07-28T00:00:00Z", "venue:binance", 3.0},
		},
	}
}

func testContext(ids ...string) map[string]any {
	times := []string{"2026-07-27T23:59:00Z", "2026-07-28T00:00:00Z"}
	periods := make(map[string][]string, len(ids))
	for _, id := range ids {
		periods[id] = append([]string(nil), times...)
	}
	return map[string]any{
		"period_time": int64(1785196800), "frequency": "1m", "subject_id": "BTC",
		"period_times_by_factor": periods,
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func errUnexpectedResult(results []ItemResult) error {
	return &unexpectedResultError{results: results}
}

type unexpectedResultError struct{ results []ItemResult }

func (e *unexpectedResultError) Error() string { return "unexpected executor results" }

func TestPoolReadyReflectsLifecycle(t *testing.T) {
	executor := newTestExecutor(t, 1, 5*time.Second)
	if !executor.Ready() {
		t.Fatal("warmed executor must be ready")
	}
	if err := executor.Close(); err != nil {
		t.Fatal(err)
	}
	if executor.Ready() {
		t.Fatal("closed executor must not be ready")
	}
	var missing *Pool
	if missing.Ready() {
		t.Fatal("nil executor must not be ready")
	}
}
