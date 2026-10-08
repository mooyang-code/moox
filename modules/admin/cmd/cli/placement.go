package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/mooyang-code/moox/modules/admin/internal/service/placement"
)

// 离线恢复命令（设计文档 3.8）：经 SSH 在 control 上执行，直接修改数据库，不依赖 Admin 或主机网关。
// 下一次快照会下发新的路由，用于处理误停用或数据异常。
//
//	moox-admin-cli placement set-status --host <主机> --component <组件> --status enabled|disabled
//	moox-admin-cli host set-status --host <主机> --status enabled|disabled

func isPlacementCommand(args []string) bool {
	return len(args) > 1 && (args[1] == "placement" || args[1] == "host")
}

func runPlacementCommand(args []string, stdout, stderr io.Writer) error {
	if len(args) < 2 || args[1] != "set-status" {
		return fmt.Errorf("用法：%s set-status ...", args[0])
	}
	fs := flag.NewFlagSet(args[0], flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath, host, component, status := defaultInitDBPath, "", "", ""
	fs.StringVar(&dbPath, "db-path", dbPath, "SQLite 数据库路径")
	fs.StringVar(&host, "host", "", "主机 ID")
	fs.StringVar(&component, "component", "", "组件 ID")
	fs.StringVar(&status, "status", "", "enabled 或 disabled")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}
	if host == "" || status == "" {
		return errors.New("需要 --host 和 --status")
	}
	db, err := openPlacementDB(dbPath)
	if err != nil {
		return err
	}
	defer closeAdminCLIDB(db)
	service := placement.NewService(db, nil)
	ctx := context.Background()
	if args[0] == "host" {
		updated, err := service.SetHostStatus(ctx, host, status)
		if err != nil {
			return err
		}
		return writeJSON(stdout, map[string]any{"status": "ok", "host": updated.HostID, "host_status": updated.Status})
	}
	if component == "" {
		return errors.New("placement set-status 需要 --component")
	}
	updated, err := service.SetPlacementStatus(ctx, host, component, status)
	if err != nil {
		return err
	}
	return writeJSON(stdout, map[string]any{"status": "ok", "host": updated.HostID, "component": updated.ComponentID, "placement_status": updated.Status})
}
