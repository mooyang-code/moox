package main

import (
	"flag"
	"fmt"

	"github.com/mooyang-code/moox/modules/monitor/internal/config"
	monstorage "github.com/mooyang-code/moox/modules/monitor/internal/storage"
	"github.com/mooyang-code/moox/modules/monitor/schema"
)

func runInit(args []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	dbPath := flags.String("db-path", config.Default().Database.Path, "monitor sqlite database path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	mgr, err := monstorage.Open(*dbPath)
	if err != nil {
		return err
	}
	defer mgr.Close()
	if err := mgr.ApplySchema(schema.SQL()); err != nil {
		return err
	}
	fmt.Printf("monitor schema initialized: %s\n", *dbPath)
	return nil
}
