package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/glebarez/sqlite"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
	"gorm.io/gorm"
)

func runInit(_ context.Context, cfg cliConfig, out io.Writer) error {
	db, err := openFactorDB(cfg.DBPath)
	if err != nil {
		return err
	}
	sqlDB, err := db.DB()
	if err == nil {
		defer sqlDB.Close()
	}
	if err := db.Exec(factorschema.AllSQL()).Error; err != nil {
		return fmt.Errorf("apply factor schema: %w", err)
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"ok":       true,
		"database": cfg.DBPath,
		"tables":   []string{"t_factor_defs", "t_factor_bindings", "t_factor_runs"},
	})
}

func openFactorDB(dbPath string) (*gorm.DB, error) {
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("open factor database: %w", err)
	}
	return db, nil
}
