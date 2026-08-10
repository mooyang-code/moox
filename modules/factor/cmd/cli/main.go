package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	trpc "trpc.group/trpc-go/trpc-go"
)

type cliConfig struct {
	Command           string
	ConfigPath        string
	DBPath            string
	FactorsDir        string
	File              string
	FactorID          string
	InputColumns      []string
	Outputs           []string
	ParamsJSON        string
	LookbackPeriods   int
	SpaceID           string
	DatasetID         string
	ViewID            string
	SubjectID         string
	Freq              string
	StartTime         time.Time
	EndTime           time.Time
	FactorIDs         []string
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
	default:
		return fmt.Errorf("unknown command %q", cfg.Command)
	}
}

func parseArgs(args []string) (cliConfig, error) {
	if len(args) == 0 {
		return cliConfig{}, errors.New("command is required")
	}
	cfg := cliConfig{Command: args[0], ParamsJSON: "{}"}
	switch args[0] {
	case "init":
		cfg.DBPath = "./data/factor/factor.db"
		fs := newFlagSet("init")
		fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "factor sqlite database")
		if err := fs.Parse(args[1:]); err != nil {
			return cliConfig{}, err
		}
	case "import":
		cfg.DBPath = "./data/factor/factor.db"
		cfg.FactorsDir = "./factors"
		var inputColumns, outputs string
		fs := newFlagSet("import")
		fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "factor sqlite database")
		fs.StringVar(&cfg.FactorsDir, "factors-dir", cfg.FactorsDir, "factor source directory")
		fs.StringVar(&cfg.File, "file", "", "single Python factor file")
		fs.StringVar(&cfg.FactorID, "factor-id", "", "factor id")
		fs.StringVar(&inputColumns, "input-columns", "", "comma-separated input columns")
		fs.StringVar(&outputs, "outputs", "", "comma-separated output columns")
		fs.StringVar(&cfg.ParamsJSON, "params-json", "{}", "factor parameter JSON object")
		fs.IntVar(&cfg.LookbackPeriods, "lookback-periods", 0, "input lookback periods")
		if err := fs.Parse(args[1:]); err != nil {
			return cliConfig{}, err
		}
		var err error
		cfg.InputColumns, err = parseImportCSV("--input-columns", inputColumns)
		if err != nil {
			return cliConfig{}, err
		}
		cfg.Outputs, err = parseImportCSV("--outputs", outputs)
		if err != nil {
			return cliConfig{}, err
		}
	case "run-once":
		cfg.ConfigPath = "./config/app.yaml"
		var startTime, endTime string
		var factors string
		fs := newFlagSet("run-once")
		fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "factor application config")
		fs.StringVar(&cfg.DBPath, "db", "", "factor sqlite database (overrides config)")
		fs.StringVar(&cfg.FactorsDir, "factors-dir", "", "factor source directory (overrides config)")
		fs.StringVar(&cfg.SpaceID, "space", "", "space id")
		fs.StringVar(&cfg.DatasetID, "dataset", "", "source dataset id")
		fs.StringVar(&cfg.ViewID, "view-id", "", "source View id (defaults to dataset id)")
		fs.StringVar(&cfg.SubjectID, "subject", "", "subject id")
		fs.StringVar(&cfg.Freq, "freq", "", "frequency")
		fs.StringVar(&startTime, "start-time", "", "inclusive start time RFC3339")
		fs.StringVar(&endTime, "end-time", "", "exclusive end time RFC3339")
		fs.StringVar(&factors, "factors", "", "comma-separated factor ids")
		if err := fs.Parse(args[1:]); err != nil {
			return cliConfig{}, err
		}
		var err error
		if cfg.StartTime, err = time.Parse(time.RFC3339Nano, startTime); err != nil {
			return cliConfig{}, fmt.Errorf("parse --start-time: %w", err)
		}
		if cfg.EndTime, err = time.Parse(time.RFC3339Nano, endTime); err != nil {
			return cliConfig{}, fmt.Errorf("parse --end-time: %w", err)
		}
		if !cfg.StartTime.Before(cfg.EndTime) {
			return cliConfig{}, errors.New("--start-time must be before --end-time")
		}
		cfg.FactorIDs = parseStringCSV(factors)
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

func parseImportCSV(flagName, raw string) ([]string, error) {
	parts := strings.Split(raw, ",")
	out := make([]string, len(parts))
	for i, part := range parts {
		out[i] = strings.TrimSpace(part)
		if out[i] == "" {
			return nil, fmt.Errorf("%s contains an empty value", flagName)
		}
	}
	return out, nil
}
