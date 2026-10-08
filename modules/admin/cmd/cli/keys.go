package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	trpc "trpc.group/trpc-go/trpc-go"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/mooyang-code/moox/modules/admin/internal/service/keys"
	secretdao "github.com/mooyang-code/moox/modules/admin/internal/service/secret/dao"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

// moox-admin-cli keys：调用方签名密钥的生成、导出与轮换（设计文档 3.5）。部署脚本经 SSH 在 control 上调用。
//
//	keys ensure  --caller <身份> [--principal] [--out <文件>]   有就复用，没有就生成
//	keys export  --caller <身份> [--principal] --out <文件>     导出当前签名密钥
//	keys rotate  --caller <身份> [--principal] [--out <文件>]   生成新 KeyID，新旧并存
//	keys retire  --caller <身份> [--principal] --key-id <KeyID> 确认新密钥生效后停用旧的
//	keys list    [--principal]
//	keys export-principals --out <文件>                         外部接入用的外部调用方密钥集

func isKeysCommand(args []string) bool { return len(args) > 1 && args[1] == "keys" }

func runKeysCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 {
		return errors.New("缺少 keys 子命令：ensure、export、rotate、retire、list、export-principals")
	}
	fs := flag.NewFlagSet("keys", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath, keyFile, caller, out, keyID := defaultInitDBPath, "", "", "", ""
	principal := false
	fs.StringVar(&dbPath, "db-path", dbPath, "SQLite 数据库路径")
	fs.StringVar(&keyFile, "encryption-key-file", "", "0600 的 Admin 加密密钥文件")
	fs.StringVar(&caller, "caller", "", "调用方身份，例如 collector、host-gateway@storage")
	fs.BoolVar(&principal, "principal", false, "外部调用方（只登记在外部接入上）")
	fs.StringVar(&out, "out", "", "密钥文件输出路径（0600）")
	fs.StringVar(&keyID, "key-id", "", "要停用的 KeyID")
	sub := args[1]
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if err := loadCLIKey(dbPath, keyFile); err != nil {
		return err
	}
	db, err := openPlacementDB(dbPath)
	if err != nil {
		return err
	}
	defer closeAdminCLIDB(db)
	service := keys.NewService(secretdao.NewSecretDAO(db))
	category := keys.CategoryCaller
	if principal {
		category = keys.CategoryPrincipal
	}
	ctx := trpc.BackgroundContext()
	needCaller := func() error {
		if caller == "" {
			return fmt.Errorf("keys %s 需要 --caller", sub)
		}
		if err := validateKeyIdentity(category, caller); err != nil {
			return err
		}
		return nil
	}
	switch sub {
	case "ensure", "rotate":
		if err := needCaller(); err != nil {
			return err
		}
		var key keys.Key
		created := true
		if sub == "ensure" {
			key, created, err = service.Ensure(ctx, category, caller)
		} else {
			key, err = service.Rotate(ctx, category, caller)
		}
		if err != nil {
			return err
		}
		if out != "" {
			if err := writeKeyFile(out, key); err != nil {
				return err
			}
		}
		return writeJSON(stdout, map[string]any{"status": "ok", "caller": key.Caller, "key_id": key.KeyID, "created": created})
	case "export":
		if err := needCaller(); err != nil {
			return err
		}
		if out == "" {
			return errors.New("keys export 需要 --out")
		}
		key, err := service.SigningKey(ctx, category, caller)
		if err != nil {
			return err
		}
		if err := writeKeyFile(out, key); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "ok", "caller": key.Caller, "key_id": key.KeyID})
	case "retire":
		if err := needCaller(); err != nil {
			return err
		}
		if keyID == "" {
			return errors.New("keys retire 需要 --key-id")
		}
		if err := service.Retire(ctx, category, caller, keyID); err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "ok", "caller": caller, "retired": keyID})
	case "list":
		all, err := service.List(ctx, category, caller)
		if err != nil {
			return err
		}
		items := make([]map[string]any, 0, len(all))
		for _, key := range all {
			items = append(items, map[string]any{"caller": key.Caller, "key_id": key.KeyID, "active": key.Active})
		}
		return writeJSON(stdout, map[string]any{"status": "ok", "keys": items})
	case "export-principals":
		if out == "" {
			return errors.New("keys export-principals 需要 --out")
		}
		count, err := exportPrincipalKeys(ctx, service, out)
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "ok", "out": out, "keys": count})
	default:
		return fmt.Errorf("未知的 keys 子命令 %q", sub)
	}
}

// validateKeyIdentity 只允许组件目录中定义的身份，避免为拼错的名字生成密钥。
func validateKeyIdentity(category, caller string) error {
	catalog := servicecatalog.Default()
	if category == keys.CategoryPrincipal {
		if _, ok := catalog.Principal(caller); !ok {
			return fmt.Errorf("外部调用方 %q 不在组件目录中", caller)
		}
		return nil
	}
	if !catalog.KnownCaller(caller) {
		return fmt.Errorf("调用方 %q 不在组件目录中", caller)
	}
	return nil
}

func writeKeyFile(path string, key keys.Key) error {
	raw, err := gatewayauth.MarshalCallerKey(key.File())
	if err != nil {
		return err
	}
	return pki.WriteSecretFile(path, raw)
}

// exportPrincipalKeys 为组件目录中的每个外部调用方确保有密钥，并导出全部有效密钥。
func exportPrincipalKeys(ctx context.Context, service *keys.Service, out string) (int, error) {
	catalog := servicecatalog.Default()
	callers := make([]string, 0, len(catalog.Principals))
	for _, principal := range catalog.Principals {
		if _, _, err := service.Ensure(ctx, keys.CategoryPrincipal, principal.ID); err != nil {
			return 0, err
		}
		callers = append(callers, principal.ID)
	}
	active, err := service.VerificationKeys(ctx, keys.CategoryPrincipal, callers)
	if err != nil {
		return 0, err
	}
	files := make([]gatewayauth.CallerKey, 0, len(active))
	for _, key := range active {
		files = append(files, key.File())
	}
	raw, err := gatewayauth.MarshalKeySet(files)
	if err != nil {
		return 0, err
	}
	return len(files), pki.WriteSecretFile(out, raw)
}

func openPlacementDB(dbPath string) (*gorm.DB, error) {
	if err := ensureAdminSchema(dbPath); err != nil {
		return nil, err
	}
	return openAdminCLIDBWithPragmas(dbPath)
}
