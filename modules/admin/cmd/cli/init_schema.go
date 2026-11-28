package main

import (
	"encoding/json"
	"fmt"
	"io"

	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"github.com/mooyang-code/moox/packages/sqlitedb"
)

const defaultInitDBPath = "./data/admin.db"

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
	db, err := sqlitedb.OpenGORM(sqlitedb.Options{
		Path:          dbPath,
		Synchronous:   "FULL",
		BusyTimeoutMS: 10000,
	})
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer closeAdminCLIDB(db)
	if err := sqlitedb.EnsureSchemaGORM(db, dbPath, "admin", rawSQL, nil); err != nil {
		return fmt.Errorf("apply schema: %w", err)
	}
	return nil
}

func ensureAdminSchema(dbPath string) error {
	return applySchema(dbPath, adminschema.AdminSQL())
}
