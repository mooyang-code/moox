package main

import (
	"bytes"
	"github.com/glebarez/sqlite"
	adminschema "github.com/mooyang-code/moox/modules/admin/schema"
	"gorm.io/gorm"
	"os"
	"path/filepath"
	"testing"
)

func TestEventBusCredentialsEnsureIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "admin.db")
	keyPath := filepath.Join(dir, "key")
	if err := os.WriteFile(keyPath, []byte("test-encryption-key-for-eventbus"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := applySchema(dbPath, adminschema.AdminSQL()); err != nil {
		t.Fatal(err)
	}
	args := []string{"eventbus-credentials", "ensure", "--db-path", dbPath, "--encryption-key-file", keyPath, "--public-ip", "203.0.113.10"}
	var out bytes.Buffer
	if err := runEventBusCredentialsCommand(args, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	first := out.String()
	out.Reset()
	if err := runEventBusCredentialsCommand(args, &out, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if first != out.String() {
		t.Fatalf("ensure metadata changed: %q vs %q", first, out.String())
	}
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Table("t_secrets").Where("c_category = ? AND c_provider = ? AND c_is_deleted = 0", "eventbus", "moox_eventbus").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 8 {
		t.Fatalf("eventbus records=%d, want 8", count)
	}
}
