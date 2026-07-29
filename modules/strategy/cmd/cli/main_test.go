package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandsAreNamed(t *testing.T) {
	for _, v := range []string{"validate", "run-once"} {
		if v == "" {
			t.Fatal()
		}
	}
}

func TestResolveTriggerUsesFinalHistoryBarByDefault(t *testing.T) {
	got, err := resolveTrigger("", []map[string]any{
		{"time": "2026-07-29T09:59:00Z"},
		{"time": "2026-07-29T10:00:00Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "2026-07-29T10:00:00Z" {
		t.Fatalf("resolveTrigger() = %q", got)
	}
}

func TestResolveTriggerPreservesExplicitOverride(t *testing.T) {
	const explicit = "2026-07-29T10:01:00Z"
	got, err := resolveTrigger(explicit, []map[string]any{
		{"time": "2026-07-29T10:00:00Z"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != explicit {
		t.Fatalf("resolveTrigger() = %q", got)
	}
}

func TestResolveTriggerRejectsHistoryWithoutFinalTime(t *testing.T) {
	for name, data := range map[string][]map[string]any{
		"empty":        {},
		"missing time": {{"close": "1"}},
		"non-string":   {{"time": 123}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := resolveTrigger("", data); err == nil {
				t.Fatal("resolveTrigger() accepted invalid history")
			}
		})
	}
}

func TestRunOnceDerivesTriggerFromFinalHistoryBar(t *testing.T) {
	root := t.TempDir()
	manifestPath := filepath.Join(root, "strategy.yaml")
	sourcePath := filepath.Join(root, "strategy.py")
	if err := os.WriteFile(manifestPath, []byte(
		"api_version: moox.strategy/v1\n"+
			"entrypoint: strategy.py:run\n"+
			"input:\n"+
			"  history_bars: 1\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, []byte(
		"def run(context, data, params):\n"+
			"    return {'action': 'hold', 'targets': []}\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	workerPath, err := filepath.Abs("../../pyworker/worker.py")
	if err != nil {
		t.Fatal(err)
	}
	out, err := os.CreateTemp(root, "stdout")
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	errOut, err := os.CreateTemp(root, "stderr")
	if err != nil {
		t.Fatal(err)
	}
	defer errOut.Close()

	err = runCLI([]string{
		"run-once",
		"--worker", workerPath,
		"--data", `[{"time":"2026-07-29T10:00:00Z","close":"1"}]`,
		manifestPath,
		sourcePath,
	}, out, errOut)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(out.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"action": "hold"`) {
		t.Fatalf("run-once output = %s", raw)
	}
}
