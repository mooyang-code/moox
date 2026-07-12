package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/mooyang-code/moox/modules/factor/internal/registry"
	"github.com/mooyang-code/moox/modules/factor/internal/store"
	factorschema "github.com/mooyang-code/moox/modules/factor/schema"
)

func runImport(ctx context.Context, cfg cliConfig, out io.Writer) error {
	db, err := store.Open(&store.Options{Path: cfg.DBPath})
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.ApplySchema(factorschema.AllSQL()); err != nil {
		return fmt.Errorf("apply factor schema: %w", err)
	}
	entries, err := os.ReadDir(cfg.FactorsDir)
	if err != nil {
		return fmt.Errorf("read factors dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	svc := registry.NewService(db.Factors(), nil, registry.Options{
		FactorsDir:    cfg.FactorsDir,
		DefaultParams: cfg.DefaultParams,
	})
	type importedFactor struct {
		FactorID   string `json:"factor_id"`
		SourceHash string `json:"source_hash"`
	}
	imported := []importedFactor{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".py" {
			continue
		}
		factor, err := svc.ImportFactorFile(ctx, filepath.Join(cfg.FactorsDir, entry.Name()))
		if err != nil {
			return err
		}
		imported = append(imported, importedFactor{FactorID: factor.FactorID, SourceHash: factor.SourceHash})
	}
	return json.NewEncoder(out).Encode(map[string]any{"ok": true, "imported": imported})
}
