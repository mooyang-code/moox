package main

import (
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
	trpc "trpc.group/trpc-go/trpc-go"
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
		parsed, err := registry.Parse(manifest)
		if err != nil {
			return err
		}
		sum := sha256.Sum256([]byte(source))
		fmt.Fprintf(out, "valid strategy api=%s source_hash=%s\n", parsed.APIVersion, hex.EncodeToString(sum[:]))
		return nil
	case "run-once":
		fs := flag.NewFlagSet("run-once", flag.ContinueOnError)
		fs.SetOutput(errOut)
		python := fs.String("python", "python3", "Python executable")
		worker := fs.String("worker", "pyworker/worker.py", "strategy worker path")
		trigger := fs.String("trigger", "cli", "trigger bar time")
		dataJSON := fs.String("data", "[]", "complete history JSON array")
		paramsJSON := fs.String("params", "{}", "strategy parameters JSON object")
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
		if _, err := registry.Parse(manifest); err != nil {
			return err
		}
		var data []map[string]any
		if err := json.Unmarshal([]byte(*dataJSON), &data); err != nil {
			return fmt.Errorf("data: %w", err)
		}
		var params map[string]any
		if err := json.Unmarshal([]byte(*paramsJSON), &params); err != nil {
			return fmt.Errorf("params: %w", err)
		}
		strategy, err := (&registry.Service{}).Prepare(
			"cli-strategy", "cli", manifest, source,
		)
		if err != nil {
			return err
		}
		e, err := engine.New(trpc.BackgroundContext(), *python, *worker)
		if err != nil {
			return err
		}
		defer e.Close()
		if err := e.Load(trpc.BackgroundContext(), strategy); err != nil {
			return err
		}
		outValue, _, err := e.Run(trpc.BackgroundContext(), domain.ExecutionRequest{
			RequestID: "cli-run", StrategyID: strategy.ID, RunnerID: "cli",
			TriggerBarTime: *trigger, Namespace: "cli", Data: data, Params: params,
		}, strategy)
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
