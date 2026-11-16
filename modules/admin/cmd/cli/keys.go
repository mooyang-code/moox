package main

import (
	"context"
	"errors"
	"flag"
	"io"

	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
)

func isKeysCommand(args []string) bool { return len(args) > 1 && args[1] == "keys" }

// runKeysCommand 维护不属于主机的身份的签名公钥：set 登记或替换，list 列出全部（含各主机的），delete 删除。
// 私钥由使用方自己生成、自己保管；主机的签名密钥由 host-bundle 签发，不在这里改。
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
	dbPath := fs.String("db-path", defaultInitDBPath, "已有的 Admin SQLite 数据库")
	identity := fs.String("identity", "", "身份：moox-cli 或服务目录里的外部身份")
	publicKey := fs.String("public-key", "", "base64 的 32 字节 ed25519 公钥")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected keys arguments")
	}
	switch args[1] {
	case "set":
		if *identity == "" || *publicKey == "" {
			return errors.New("keys set 需要 --identity 与 --public-key")
		}
	case "delete":
		if *identity == "" || *publicKey != "" {
			return errors.New("keys delete 只需要 --identity")
		}
	case "list":
		if *identity != "" || *publicKey != "" {
			return errors.New("keys list 不接受 --identity 或 --public-key")
		}
	default:
		return errors.New("unknown keys subcommand: use set, list, or delete")
	}
	if err := validateExistingAdminDatabase(*dbPath); err != nil {
		return err
	}
	db, err := openAdminCLIDB(*dbPath)
	if err != nil {
		return err
	}
	defer closeAdminCLIDB(db)
	store, err := keys.NewStore(db)
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch args[1] {
	case "set":
		if err := store.SetIdentity(ctx, *identity, *publicKey); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]string{"status": "ok", "identity": *identity, "public_key": *publicKey})
	case "delete":
		if err := store.DeleteIdentity(ctx, *identity); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]string{"status": "ok", "identity": *identity})
	}
	hosts, err := store.Hosts(ctx)
	if err != nil {
		return err
	}
	identities, err := store.Identities(ctx)
	if err != nil {
		return err
	}
	return writeJSON(stdout, map[string]any{"status": "ok", "hosts": hosts, "identities": identities})
}
