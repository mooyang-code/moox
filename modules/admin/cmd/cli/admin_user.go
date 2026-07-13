package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	mooxcrypto "github.com/mooyang-code/moox/packages/crypto"
	"gorm.io/gorm"
)

const (
	maxBcryptPasswordBytes = 72
	maxRandomSecretBytes   = 4096
)

func isAdminUserCommand(args []string) bool {
	return len(args) > 1 && args[1] == "user"
}

func isRandomSecretCommand(args []string) bool {
	return len(args) > 1 && args[1] == "random-secret"
}

func runAdminUserCommand(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	if len(args) < 2 || args[0] != "user" {
		return errors.New("expected user subcommand")
	}
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}

	fs := flag.NewFlagSet("user "+args[1], flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := defaultInitDBPath
	username := ""
	passwordStdin := false
	fs.StringVar(&dbPath, "db-path", dbPath, "SQLite database path")
	fs.StringVar(&username, "username", "", "admin username")
	fs.BoolVar(&passwordStdin, "password-stdin", false, "read password from stdin")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected user arguments: %s", strings.Join(fs.Args(), " "))
	}
	if username == "" {
		return errors.New("--username is required")
	}
	if !passwordStdin {
		return errors.New("--password-stdin is required")
	}
	password, err := readStdinPassword(stdin)
	if err != nil {
		return err
	}
	if err := ensureAdminSchema(dbPath); err != nil {
		return err
	}
	db, err := openAdminCLIDB(dbPath)
	if err != nil {
		return err
	}
	defer closeAdminCLIDB(db)

	switch args[1] {
	case "ensure":
		return ensureAdminUser(db, username, password, stdout)
	case "reset-password":
		return resetAdminUserPassword(db, username, password, stdout)
	default:
		return fmt.Errorf("unknown user subcommand %q", args[1])
	}
}

func readStdinPassword(stdin io.Reader) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(stdin, maxBcryptPasswordBytes+2))
	if err != nil {
		return "", fmt.Errorf("read password from stdin: %w", err)
	}
	password := strings.TrimSuffix(string(raw), "\n")
	if password == "" {
		return "", errors.New("password cannot be empty")
	}
	if len([]byte(password)) > maxBcryptPasswordBytes {
		return "", errors.New("password must not exceed 72 bytes")
	}
	return password, nil
}

func ensureAdminUser(db *gorm.DB, username, password string, stdout io.Writer) error {
	var count int64
	if err := db.Table("t_users").Where("c_username = ?", username).Count(&count).Error; err != nil {
		return fmt.Errorf("find admin user: %w", err)
	}
	action := "unchanged"
	if count == 0 {
		hash, err := mooxcrypto.HashPassword(password)
		if err != nil {
			return err
		}
		result := db.Exec(`INSERT INTO t_users (c_user_id, c_username, c_password_hash, c_role, c_status) VALUES (?, ?, ?, 3, 1)`, uuid.NewString(), username, hash)
		if result.Error != nil {
			return fmt.Errorf("create admin user: %w", result.Error)
		}
		action = "created"
	}
	return json.NewEncoder(stdout).Encode(map[string]any{
		"status": "ok", "command": "user.ensure", "action": action, "username": username,
	})
}

func resetAdminUserPassword(db *gorm.DB, username, password string, stdout io.Writer) error {
	hash, err := mooxcrypto.HashPassword(password)
	if err != nil {
		return err
	}
	result := db.Table("t_users").Where("c_username = ? AND c_is_deleted = 0", username).UpdateColumn("c_password_hash", hash)
	if result.Error != nil {
		return fmt.Errorf("reset admin password: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("user %q not found", username)
	}
	return json.NewEncoder(stdout).Encode(map[string]any{
		"status": "ok", "command": "user.reset-password", "action": "password_reset", "username": username,
	})
}

func runRandomSecretCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "random-secret" {
		return errors.New("expected random-secret command")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	fs := flag.NewFlagSet("random-secret", flag.ContinueOnError)
	fs.SetOutput(stderr)
	size := 32
	fs.IntVar(&size, "bytes", size, "number of random bytes")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected random-secret arguments: %s", strings.Join(fs.Args(), " "))
	}
	if size < 1 || size > maxRandomSecretBytes {
		return fmt.Errorf("--bytes must be between 1 and %d", maxRandomSecretBytes)
	}
	raw := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, raw); err != nil {
		return fmt.Errorf("generate random secret: %w", err)
	}
	return json.NewEncoder(stdout).Encode(map[string]any{
		"status": "ok", "bytes": size, "secret": hex.EncodeToString(raw),
	})
}
