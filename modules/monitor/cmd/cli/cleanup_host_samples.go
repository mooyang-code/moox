package main

import (
	"errors"
	"flag"
	"fmt"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
)

func runCleanupHostSampleTables(args []string) error {
	flags := flag.NewFlagSet("cleanup-host-sample-tables", flag.ContinueOnError)
	dbPath := flags.String("db-path", config.Default().Database.Path, "monitor sqlite database path")
	confirm := flags.Bool("confirm", false, "confirm destructive legacy table removal")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if !*confirm {
		return errors.New("refusing to remove legacy host sample tables without --confirm")
	}
	mgr, err := store.Open(*dbPath)
	if err != nil {
		return err
	}
	defer mgr.Close()
	if err := mgr.DropLegacyHostSampleTables(); err != nil {
		return err
	}
	fmt.Printf("legacy host sample tables removed: %s\n", *dbPath)
	return nil
}
