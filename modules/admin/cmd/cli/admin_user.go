package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"gorm.io/gorm"
)

const maxBcryptPasswordBytes = 72

func isAdminUserCommand(args []string) bool {
	return len(args) > 1 && args[1] == "user"
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
	case "set-password":
		return setAdminPassword(db, username, password, stdout)
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
		hash, err := mooxsecurity.HashPassword(password)
		if err != nil {
			return err
		}
		result := db.Exec(`INSERT INTO t_users (c_user_id, c_username, c_password_hash) VALUES (?, ?, ?)`, uuid.NewString(), username, hash)
		if result.Error != nil {
			return fmt.Errorf("create admin user: %w", result.Error)
		}
		action = "created"
	}
	return json.NewEncoder(stdout).Encode(map[string]any{
		"status": "ok", "command": "user.ensure", "action": action, "username": username,
	})
}

// setAdminPassword 改一个已有管理员的密码（从 stdin 读）；已登录的会话不受影响，下次登录用新密码。
// 改完后把秘密文件里的 admin.password 一起改掉（moox-cli config set），setup init 才能用它核对登录。
func setAdminPassword(db *gorm.DB, username, password string, stdout io.Writer) error {
	hash, err := mooxsecurity.HashPassword(password)
	if err != nil {
		return err
	}
	result := db.Exec(`UPDATE t_users SET c_password_hash = ?, c_mtime = CURRENT_TIMESTAMP WHERE c_username = ?`, hash, username)
	if result.Error != nil {
		return fmt.Errorf("update admin password: %w", result.Error)
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("管理员 %q 不存在", username)
	}
	return json.NewEncoder(stdout).Encode(map[string]any{
		"status": "ok", "command": "user.set-password", "action": "updated", "username": username,
	})
}
