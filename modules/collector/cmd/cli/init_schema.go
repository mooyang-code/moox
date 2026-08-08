package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/ruleseed"
	"github.com/mooyang-code/moox/modules/collector/internal/store"
	collectorschema "github.com/mooyang-code/moox/modules/collector/schema"
)

const defaultInitDBPath = "./data/moox_collector.db"

type initResult struct {
	Module         string `json:"module"`
	Action         string `json:"action"`
	Status         string `json:"status"`
	DBPath         string `json:"db_path"`
	RulesCreated   int    `json:"rules_created"`
	RulesUnchanged int    `json:"rules_unchanged"`
}

func isInitCommand(args []string) bool {
	return len(args) > 1 && args[1] == "init"
}

func runInitCommand(args []string, stdout io.Writer, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "init" {
		return fmt.Errorf("expected init command")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := defaultInitDBPath
	fs.StringVar(&dbPath, "db-path", dbPath, "SQLite database path")
	seedFile := ""
	fs.StringVar(&seedFile, "seed-file", seedFile, "built-in Collector rule seed YAML")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected init arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err := applySchema(dbPath, collectorschema.AllSQL()); err != nil {
		return err
	}
	result := initResult{
		Module: "collector",
		Action: "init",
		Status: "ok",
		DBPath: dbPath,
	}
	if strings.TrimSpace(seedFile) != "" {
		summary, err := seedRules(dbPath, seedFile)
		if err != nil {
			return err
		}
		result.RulesCreated = summary.Created
		result.RulesUnchanged = summary.Unchanged
	}
	return json.NewEncoder(stdout).Encode(result)
}

func printInitError(stderr io.Writer, err error) {
	if stderr == nil {
		stderr = io.Discard
	}
	_ = json.NewEncoder(stderr).Encode(map[string]string{
		"error":   "init_failed",
		"message": err.Error(),
	})
}

func applySchema(dbPath string, rawSQL string) error {
	if dbPath == "" {
		return fmt.Errorf("db path is required")
	}
	db, err := store.Open(&store.Options{Path: dbPath})
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer db.Close()
	if err := db.ApplySchema(rawSQL); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

func seedRules(dbPath string, seedFile string) (ruleseed.SeedSummary, error) {
	rules, err := ruleseed.LoadFile(seedFile)
	if err != nil {
		return ruleseed.SeedSummary{}, err
	}
	db, err := store.Open(&store.Options{Path: dbPath})
	if err != nil {
		return ruleseed.SeedSummary{}, fmt.Errorf("open database for rule seed: %w", err)
	}
	defer db.Close()
	summary, err := ruleseed.SeedMissing(context.Background(), db.TaskRules(), rules)
	if err != nil {
		return ruleseed.SeedSummary{}, err
	}
	return summary, nil
}
