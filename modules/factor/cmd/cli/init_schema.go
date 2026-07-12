package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
)

func runInit(_ context.Context, cfg cliConfig, out io.Writer) error {
	db, err := store.Open(&store.Options{Path: cfg.DBPath})
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.ApplySchema(factorschema.AllSQL()); err != nil {
		return fmt.Errorf("apply factor schema: %w", err)
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"ok":       true,
		"database": cfg.DBPath,
		"tables":   []string{"t_factor_defs", "t_factor_bindings"},
	})
}
