package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	trpc "trpc.group/trpc-go/trpc-go"
)

type cliConfig struct {
	Command           string
	DBPath            string
	FactorsDir        string
	DefaultParams     []int
	SpaceID           string
	DatasetID         string
	TargetDataset     string
	SubjectID         string
	Freq              string
	BarTime           time.Time
	FactorIDs         []string
	ReplayInput       string
	ReplayStart       time.Time
	ReplayEnd         time.Time
	FactorVersion     string
	TargetRunID       string
	TaskID            string
	FactorSourcePaths map[string]string
}

func main() {
	if err := run(trpc.BackgroundContext(), os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "{\"ok\":false,\"error\":%q}\n", err.Error())
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out io.Writer) error {
	cfg, err := parseArgs(args)
	if err != nil {
		return err
	}
	switch cfg.Command {
	case "init":
		return runInit(ctx, cfg, out)
	case "import":
		return runImport(ctx, cfg, out)
	case "run-once":
		return runOnce(ctx, cfg, out)
	case "replay":
		return runReplay(ctx, cfg, out)
	default:
		return fmt.Errorf("unknown command %q", cfg.Command)
	}
}

func parseArgs(args []string) (cliConfig, error) {
	if len(args) == 0 {
		return cliConfig{}, errors.New("command is required")
	}
	cfg := cliConfig{Command: args[0], DBPath: "./data/factor/factor.db", FactorsDir: "./factors", DefaultParams: []int{20}}
	switch args[0] {
	case "init":
		fs := newFlagSet("init")
		fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "factor sqlite database")
		if err := fs.Parse(args[1:]); err != nil {
			return cliConfig{}, err
		}
	case "import":
		var params string
		fs := newFlagSet("import")
		fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "factor sqlite database")
		fs.StringVar(&cfg.FactorsDir, "factors-dir", cfg.FactorsDir, "factor source directory")
		fs.StringVar(&params, "default-params", "20", "comma-separated default params")
		if err := fs.Parse(args[1:]); err != nil {
			return cliConfig{}, err
		}
		parsed, err := parseIntCSV(params)
		if err != nil {
			return cliConfig{}, err
		}
		cfg.DefaultParams = parsed
	case "run-once":
		var barTime string
		var factors string
		fs := newFlagSet("run-once")
		fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "factor sqlite database")
		fs.StringVar(&cfg.FactorsDir, "factors-dir", cfg.FactorsDir, "factor source directory")
		fs.StringVar(&cfg.SpaceID, "space", "", "space id")
		fs.StringVar(&cfg.DatasetID, "dataset", "", "source dataset id")
		fs.StringVar(&cfg.SubjectID, "subject", "", "subject id")
		fs.StringVar(&cfg.Freq, "freq", "", "frequency")
		fs.StringVar(&barTime, "bar-time", "", "bar time RFC3339")
		fs.StringVar(&factors, "factors", "", "comma-separated factor ids")
		if err := fs.Parse(args[1:]); err != nil {
			return cliConfig{}, err
		}
		if barTime != "" {
			parsed, err := time.Parse(time.RFC3339, barTime)
			if err != nil {
				return cliConfig{}, fmt.Errorf("parse --bar-time: %w", err)
			}
			cfg.BarTime = parsed
		}
		cfg.FactorIDs = parseStringCSV(factors)
	case "replay":
		var start, end string
		fs := newFlagSet("replay")
		fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "factor sqlite database")
		fs.StringVar(&cfg.ReplayInput, "input", "", "JSONL replay event file")
		fs.StringVar(&cfg.SpaceID, "space", "", "space id")
		fs.StringVar(&cfg.DatasetID, "dataset", "", "source dataset id")
		fs.StringVar(&start, "start", "", "replay start time RFC3339")
		fs.StringVar(&end, "end", "", "replay end time RFC3339")
		fs.StringVar(&cfg.FactorVersion, "factor-version", "", "factor version")
		fs.StringVar(&cfg.TargetRunID, "target-run-id", "", "target run id")
		if err := fs.Parse(args[1:]); err != nil {
			return cliConfig{}, err
		}
		var err error
		if cfg.ReplayStart, err = time.Parse(time.RFC3339, start); err != nil {
			return cliConfig{}, fmt.Errorf("parse --start: %w", err)
		}
		if cfg.ReplayEnd, err = time.Parse(time.RFC3339, end); err != nil {
			return cliConfig{}, fmt.Errorf("parse --end: %w", err)
		}
	default:
		return cliConfig{}, fmt.Errorf("unknown command %q", args[0])
	}
	return cfg, nil
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parseIntCSV(raw string) ([]int, error) {
	parts := parseStringCSV(raw)
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		v, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("parse int %q: %w", part, err)
		}
		out = append(out, v)
	}
	return out, nil
}

func parseStringCSV(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if v := strings.TrimSpace(part); v != "" {
			out = append(out, v)
		}
	}
	return out
}
