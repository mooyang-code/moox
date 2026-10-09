package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	"gorm.io/gorm"
)

func isKeysCommand(args []string) bool { return len(args) > 1 && args[1] == "keys" }

func runKeysCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 || args[0] != "keys" {
		return errors.New("expected keys subcommand")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	fs := flag.NewFlagSet("keys "+args[1], flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db-path", defaultInitDBPath, "existing Admin SQLite database")
	keyFile := fs.String("encryption-key-file", "", "existing 0600 Admin encryption key file")
	caller := fs.String("caller", "", "canonical caller identity")
	keyID := fs.String("key-id", "", "old KeyID to retire")
	outputDir := fs.String("output-dir", "", "private credential output directory")
	all := fs.Bool("all", false, "ensure all catalog and registered host callers")
	confirm := fs.Bool("confirm", false, "confirm the replacement signing key is in use")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected keys arguments")
	}
	// Reject irrelevant/unknown options before opening files or mutating keys.
	switch args[1] {
	case "ensure":
		if (*all == (*caller != "")) || *outputDir != "" || *keyID != "" || *confirm {
			return errors.New("keys ensure requires exactly one of --caller or --all")
		}
	case "export":
		if *caller == "" || *outputDir == "" || *all || *keyID != "" || *confirm {
			return errors.New("keys export requires --caller and --output-dir")
		}
	case "export-access":
		if *outputDir == "" || *caller != "" || *all || *keyID != "" || *confirm {
			return errors.New("keys export-access requires --output-dir")
		}
	case "rotate", "info":
		if *caller == "" || *outputDir != "" || *all || *keyID != "" || *confirm {
			return errors.New("keys " + args[1] + " requires --caller")
		}
	case "retire":
		if *caller == "" || *keyID == "" || !*confirm || *outputDir != "" || *all {
			return errors.New("keys retire requires --caller, --key-id, and --confirm after verifying the replacement")
		}
	default:
		return errors.New("unknown keys subcommand: use ensure, info, export, export-access, rotate, or retire")
	}
	if *keyFile == "" {
		return errors.New("--encryption-key-file is required")
	}
	master, err := readPrivateFile(*keyFile, 8192)
	if err != nil {
		return fmt.Errorf("read Admin encryption key: %w", err)
	}
	info, err := os.Lstat(*dbPath)
	if err != nil {
		return fmt.Errorf("stat Admin database: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("Admin database must be an existing regular file")
	}
	db, err := gorm.Open(sqlite.Open(initSQLiteDSN(*dbPath)), &gorm.Config{})
	if err != nil {
		return err
	}
	defer closeAdminCLIDB(db)
	store, err := keys.NewStore(db, strings.TrimSpace(string(master)))
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch args[1] {
	case "ensure":
		if *all {
			items, err := store.EnsureAll(ctx)
			if err != nil {
				return err
			}
			return writeJSON(stdout, map[string]any{"status": "ok", "callers": items})
		}
		key, err := store.Ensure(ctx, *caller)
		if err != nil {
			return err
		}
		return writeJSON(stdout, key)
	case "info":
		info, err := store.Info(ctx, *caller)
		if err != nil {
			return err
		}
		return writeJSON(stdout, info)
	case "rotate":
		key, err := store.Rotate(ctx, *caller)
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{
			"status": "ok", "caller": key.Caller, "key_id": key.KeyID,
			"next": "wait for gateway/Access verifier updates, export and redeploy the caller, verify it, then retire the old KeyID",
		})
	case "retire":
		if err := store.Retire(ctx, *caller, *keyID); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]string{"status": "ok", "caller": *caller, "retired_key_id": *keyID})
	case "export":
		key, err := store.Current(ctx, *caller)
		if err != nil {
			return err
		}
		root, err := privateOutputRoot(*outputDir)
		if err != nil {
			return err
		}
		defer root.Close()
		name := "caller-" + key.Caller + ".key"
		if err := writePrivateFile(root, name, []byte(key.Credentials().Secret+"\n")); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]string{"caller": key.Caller, "key_id": key.KeyID, "key_file": filepath.Join(*outputDir, name)})
	case "export-access":
		return exportAccessKeys(ctx, store, *outputDir, stdout)
	}
	return nil
}

func exportAccessKeys(ctx context.Context, store *keys.Store, dir string, stdout io.Writer) error {
	items, err := store.ExternalVerification(ctx)
	if err != nil {
		return err
	}
	root, err := privateOutputRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	type credential struct {
		KeyID      string `json:"key_id"`
		Caller     string `json:"caller"`
		SecretFile string `json:"secret_file"`
	}
	file := struct {
		Version int          `json:"version"`
		Keys    []credential `json:"credentials"`
	}{Version: 1}
	for _, item := range items {
		name := "caller-" + item.Caller + "-" + item.KeyID + ".key"
		if err := writePrivateFile(root, name, item.Secret); err != nil {
			return err
		}
		file.Keys = append(file.Keys, credential{KeyID: item.KeyID, Caller: item.Caller, SecretFile: name})
	}
	raw, err := json.Marshal(file)
	if err != nil {
		return err
	}
	// Publish the registry last. Versioned key files keep the old registry valid
	// if any write fails. Removed keys are absent from the next active registry.
	if err := writePrivateFile(root, "access-verification.json", append(raw, '\n')); err != nil {
		return err
	}
	return writeJSON(stdout, map[string]any{"status": "ok", "registry_file": filepath.Join(dir, "access-verification.json"), "key_count": len(items)})
}
