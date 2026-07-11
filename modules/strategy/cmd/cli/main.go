package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/mooyang-code/moox/modules/strategy/internal/domain"
	"github.com/mooyang-code/moox/modules/strategy/internal/engine"
	"github.com/mooyang-code/moox/modules/strategy/internal/registry"
)

func main() {
	if err := runCLI(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runCLI(args []string, out, errOut *os.File) error {
	if len(args) == 0 {
		return errors.New("usage: strategy validate <manifest.yaml> <source.py> | run-once <manifest.yaml> <source.py>")
	}
	switch args[0] {
	case "validate":
		if len(args) != 3 {
			return errors.New("usage: strategy validate <manifest.yaml> <source.py>")
		}
		manifest, source, err := readPackage(args[1], args[2])
		if err != nil {
			return err
		}
		m, err := registry.Parse(manifest)
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(source))
		fmt.Fprintf(out, "valid strategy %s@%s source_hash=%s\n", m.ID, m.Version, hex.EncodeToString(sum[:]))
		return nil
	case "run-once":
		fs := flag.NewFlagSet("run-once", flag.ContinueOnError)
		fs.SetOutput(errOut)
		python := fs.String("python", "python3", "Python executable")
		worker := fs.String("worker", "pyworker/worker.py", "strategy worker path")
		trigger := fs.String("trigger", "cli", "trigger bar time")
		stateJSON := fs.String("state", "{}", "previous state JSON")
		if len(args) < 3 {
			return errors.New("usage: strategy run-once [flags] <manifest.yaml> <source.py>")
		}
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		pos := fs.Args()
		if len(pos) != 2 {
			return errors.New("usage: strategy run-once [flags] <manifest.yaml> <source.py>")
		}
		manifest, source, err := readPackage(pos[0], pos[1])
		if err != nil {
			return err
		}
		m, err := registry.Parse(manifest)
		if err != nil {
			return err
		}
		var state map[string]any
		if err := json.Unmarshal([]byte(*stateJSON), &state); err != nil {
			return fmt.Errorf("state: %w", err)
		}
		sum := sha256.Sum256([]byte(source))
		definition := domain.StrategyDefinition{StrategyID: m.ID, Version: m.Version, API: m.API, ManifestYAML: manifest, SourceCode: source, SourceHash: hex.EncodeToString(sum[:])}
		e, err := engine.New(context.Background(), *python, *worker)
		if err != nil {
			return err
		}
		defer e.Close()
		if err := e.Load(context.Background(), definition); err != nil {
			return err
		}
		outValue, _, err := e.Run(context.Background(), domain.Task{RunID: "cli-run", BindingID: "cli", StrategyID: m.ID, Version: m.Version, TriggerBarTime: *trigger, PreviousState: domain.State{StateJSON: mustJSON(state)}}, definition)
		if err != nil {
			return err
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(outValue)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func readPackage(manifestPath, sourcePath string) (string, string, error) {
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		return "", "", err
	}
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return "", "", err
	}
	return string(manifest), string(source), nil
}

func mustJSON(value any) string {
	b, _ := json.Marshal(value)
	return string(b)
}
