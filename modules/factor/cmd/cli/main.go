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
	Command    string
	ConfigPath string
	DBPath     string
	TaskID     string
	File       string
	CatalogDir string
	FactorID   string
	StartTime  time.Time
	EndTime    time.Time
	Members    []string
	Subjects   []string
	RequestID  string
	RPCTarget  string
	SpaceID    string
	DatasetID  string
	Yes        bool
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
	case "purge-dataset":
		return runPurgeDataset(ctx, cfg, out)
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
	}
	fs := newFlagSet(cfg.Command)
	var start, end string
	switch cfg.Command {
	case "init":
		fs.StringVar(&cfg.DBPath, "db", cfg.DBPath, "Factor SQLite database")
	case "import":
		cfg.DBPath = ""
		fs.StringVar(&cfg.DBPath, "db", "", "Factor SQLite database (overrides config)")
		fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "Factor runtime configuration")
		fs.StringVar(&cfg.TaskID, "task", "", "also add the definition to this factor task as a disabled member")
		fs.StringVar(&cfg.File, "file", "", "YAML factor library file ([{factor_id, name, spec}])")
		fs.StringVar(&cfg.FactorID, "factor-id", "", "factor id to import (required when the file has several)")
	case "import-catalog":
		cfg.DBPath = ""
		fs.StringVar(&cfg.DBPath, "db", "", "Factor SQLite database (overrides config)")
		fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "Factor runtime configuration")
		fs.StringVar(&cfg.CatalogDir, "dir", cfg.CatalogDir, "YAML factor library directory (*.yaml)")
		fs.StringVar(&cfg.TaskID, "task", "", "also add the definitions to this factor task as disabled members")
	case "recalc":
		cfg.DBPath = ""
		fs.StringVar(&cfg.DBPath, "db", "", "Factor SQLite database (overrides config)")
		fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "Factor runtime configuration")
		fs.StringVar(&cfg.TaskID, "task", "", "factor task id")
		fs.StringVar(&cfg.RequestID, "request-id", "", "idempotent request id (generated when omitted)")
		fs.StringVar(&start, "start", "", "inclusive start timestamp RFC3339")
		fs.StringVar(&end, "end", "", "exclusive end timestamp RFC3339")
		fs.Var((*listFlag)(&cfg.Members), "member", "要补算的成员别名（可重复；不填为全部启用成员）")
		fs.Var((*listFlag)(&cfg.Subjects), "subject", "subject id (repeatable)")
	case "purge-dataset":
		cfg.DBPath = ""
		fs.StringVar(&cfg.DBPath, "db", "", "Factor SQLite database (overrides config)")
		fs.StringVar(&cfg.ConfigPath, "config", cfg.ConfigPath, "Factor runtime configuration")
		fs.StringVar(&cfg.SpaceID, "space", "", "Space of the factor result dataset")
		fs.StringVar(&cfg.DatasetID, "dataset", "", "factor result dataset id to delete (rows and metadata)")
		fs.BoolVar(&cfg.Yes, "yes", false, "confirm the irreversible deletion")
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
	if cfg.Command == "purge-dataset" {
		if strings.TrimSpace(cfg.SpaceID) == "" || strings.TrimSpace(cfg.DatasetID) == "" {
			return cliConfig{}, errors.New("--space and --dataset are required")
		}
	}
	if cfg.Command == "recalc" {
		if strings.TrimSpace(cfg.TaskID) == "" {
			return cliConfig{}, errors.New("--task is required")
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
