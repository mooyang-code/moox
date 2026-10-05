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
)

type cliConfig struct {
	Command         string
	ConfigPath      string
	DBPath          string
	SetID           string
	File            string
	CatalogDir      string
	FactorID        string
	FactorType      string
	InputColumns    []string
	Outputs         []string
	ParamsJSON      string
	LookbackPeriods int
	StartTime       time.Time
	EndTime         time.Time
	FactorIDs       []string
	Subjects        []string
	RequestID       string
	RPCTarget       string
}

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdout); err != nil {
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
		return runInit(cfg, out)
	case "import":
		return runImport(ctx, cfg, out)
	case "import-catalog":
		return runImportCatalog(ctx, cfg, out)
	case "recalc":
		return runRecalc(ctx, cfg, out)
	case "status":
		return runStatus(ctx, cfg, out)
	default:
		return fmt.Errorf("unknown command %q", cfg.Command)
	}
}

func parseArgs(args []string) (cliConfig, error) {
	if len(args) == 0 {
		return cliConfig{}, errors.New("command is required")
	}
	cfg := cliConfig{
		Command: args[0], DBPath: "./data/factor/factor.db",
		CatalogDir: "./factors", ConfigPath: "./config/app.yaml",
		FactorType: "timeseries", ParamsJSON: "{}",
	}
	fs := newFlagSet(cfg.Command)
	var inputs, outputs string
	var start, end string
	switch cfg.Command {
	case "init":
		fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "Factor SQLite database")
	case "import":
		cfg.DBPath = ""
		fs.StringVar(&cfg.DBPath, "db", "", "Factor SQLite database (overrides config)")
		fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "Factor runtime configuration")
		fs.StringVar(&cfg.SetID, "set", "", "also add the definition to this factor set as a disabled member")
		fs.StringVar(&cfg.File, "file", "", "Python factor source file")
		fs.StringVar(&cfg.FactorID, "factor-id", "", "factor id")
		fs.StringVar(&cfg.FactorType, "factor-type", cfg.FactorType, "timeseries or cross_section")
		fs.StringVar(&inputs, "inputs", "", "comma-separated input columns")
		fs.StringVar(&outputs, "outputs", "", "comma-separated output columns")
		fs.IntVar(&cfg.LookbackPeriods, "lookback", 0, "input lookback periods")
		fs.StringVar(&cfg.ParamsJSON, "params", cfg.ParamsJSON, "factor parameter JSON object")
	case "import-catalog":
		cfg.DBPath = ""
		fs.StringVar(&cfg.DBPath, "db", "", "Factor SQLite database (overrides config)")
		fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "Factor runtime configuration")
		fs.StringVar(&cfg.CatalogDir, "dir", cfg.CatalogDir, "directory containing catalog.json and Python sources")
		fs.StringVar(&cfg.SetID, "set", "", "also add the definitions to this factor set as disabled members")
	case "recalc":
		cfg.DBPath = ""
		fs.StringVar(&cfg.DBPath, "db", "", "Factor SQLite database (overrides config)")
		fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "Factor runtime configuration")
		fs.StringVar(&cfg.SetID, "set", "", "factor set id")
		fs.StringVar(&cfg.RequestID, "request-id", "", "idempotent request id (generated when omitted)")
		fs.StringVar(&start, "start", "", "inclusive start timestamp RFC3339")
		fs.StringVar(&end, "end", "", "exclusive end timestamp RFC3339")
		fs.Var((*listFlag)(&cfg.FactorIDs), "factor", "factor id (repeatable)")
		fs.Var((*listFlag)(&cfg.Subjects), "subject", "subject id (repeatable)")
	case "status":
		fs.StringVar(&cfg.RPCTarget, "target", os.Getenv("MOOX_FACTOR_RPC_TARGET"), "FactorMgr tRPC target")
	default:
		return cliConfig{}, fmt.Errorf("unknown command %q", cfg.Command)
	}
	if err := fs.Parse(args[1:]); err != nil {
		return cliConfig{}, err
	}
	if fs.NArg() > 0 {
		return cliConfig{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if cfg.Command == "import" {
		var err error
		cfg.InputColumns, err = parseCSV("--inputs", inputs)
		if err != nil {
			return cliConfig{}, err
		}
		cfg.Outputs, err = parseCSV("--outputs", outputs)
		if err != nil {
			return cliConfig{}, err
		}
	}
	if cfg.Command == "recalc" {
		if strings.TrimSpace(cfg.SetID) == "" {
			return cliConfig{}, errors.New("--set is required")
		}
		var err error
		cfg.StartTime, err = parseTimeFlag("--start", start)
		if err != nil {
			return cliConfig{}, err
		}
		cfg.EndTime, err = parseTimeFlag("--end", end)
		if err != nil {
			return cliConfig{}, err
		}
		if !cfg.StartTime.Before(cfg.EndTime) {
			return cliConfig{}, errors.New("--start must be before --end")
		}
	}
	return cfg, nil
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func parseCSV(name, raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	return normalizeCLIList(name, strings.Split(raw, ","))
}

func parseTimeFlag(name, raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, fmt.Errorf("%s is required", name)
	}
	timeValue, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %s as RFC3339: %w", name, err)
	}
	return timeValue.UTC(), nil
}

type listFlag []string

func (v *listFlag) Set(raw string) error {
	values, err := normalizeCLIList("list", strings.Split(raw, ","))
	if err != nil {
		return err
	}
	*v = append(*v, values...)
	return nil
}

func (v *listFlag) String() string { return strings.Join(*v, ",") }
