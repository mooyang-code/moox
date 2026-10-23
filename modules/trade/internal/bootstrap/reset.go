package bootstrap

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/mooyang-code/moox/modules/trade/internal/infra/store"
	"github.com/mooyang-code/moox/packages/lifecycle"
	"trpc.group/trpc-go/trpc-go/log"
)

// Trade 的重置（设计 5.2、6.1）。
//
//	data：清运行数据——订单、成交、持仓、权益点、目标与回执、影子目标、回放账本、纸面余额、操作记录；保留账户与逻辑账户（配置）；
//	all ：再清账户（交易账户、纸面配置）、逻辑账户及成员、行情标的，回到新部署的空白状态。
//
// 重置只清本地账本，**交易所上的仓位和订单不会因此消失**，所以实盘账户还有持仓或未完成订单时一律拒绝：
// plan 阶段按本地账本预检，执行的第一步先向交易所重新对账、再核验一次——plan 到 apply 之间可能新增仓位，
// 暂停也只是不再下新单，不撤单，在途的挂单仍可能成交。需要用户先自己平仓、撤单。
// 交易所 API 密钥在 Admin 的 secrets 里，不属于 Trade 的库，重置不碰。

var (
	tradeDataTables = []string{
		"t_trade_orders", "t_order_fills", "t_trading_positions", "t_trading_position_tombstones",
		"t_account_equity_points", "t_logical_account_equity_points",
		"t_logical_account_targets", "t_logical_account_target_receipts", "t_shadow_targets",
		"t_replay_sessions", "t_replay_periods", "t_replay_orders",
		"t_paper_balance_projections", "t_paper_asset_balances", "t_operator_actions",
	}
	tradeConfigTables = []string{
		"t_trading_accounts", "t_paper_account_configs", "t_logical_accounts", "t_logical_account_members", "t_trade_instruments",
	}
)

// liveExposure 返回（订单终态：FILLED、CANCELED、PARTIALLY_CANCELED、REJECTED、EXPIRED，其余都算未完成）实盘账户（启用的）还有持仓或未完成订单的说明；没有为空。
func liveExposure(ctx context.Context, db *sql.DB) ([]string, error) {
	var out []string
	rows, err := db.QueryContext(ctx, `
		SELECT p.c_trading_account_id, p.c_exchange_symbol, p.c_signed_quantity
		FROM t_trading_positions p
		JOIN t_trading_accounts a ON a.c_space_id = p.c_space_id AND a.c_trading_account_id = p.c_trading_account_id
		WHERE a.c_execution_mode = 'LIVE' AND CAST(p.c_signed_quantity AS REAL) <> 0
		ORDER BY p.c_trading_account_id, p.c_exchange_symbol LIMIT 5`)
	if err != nil {
		return nil, fmt.Errorf("检查实盘持仓：%w", err)
	}
	for rows.Next() {
		var account, symbol, qty string
		if err := rows.Scan(&account, &symbol, &qty); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, fmt.Sprintf("实盘账户 %s 还有持仓 %s %s", account, symbol, qty))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var open int
	if err := db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM t_trade_orders o
		JOIN t_trading_accounts a ON a.c_space_id = o.c_space_id AND a.c_trading_account_id = o.c_trading_account_id
		WHERE a.c_execution_mode = 'LIVE' AND o.c_state NOT IN ('FILLED','CANCELED','PARTIALLY_CANCELED','REJECTED','EXPIRED')`).Scan(&open); err != nil {
		return nil, fmt.Errorf("检查实盘未完成订单：%w", err)
	}
	if open > 0 {
		out = append(out, fmt.Sprintf("实盘账户还有 %d 个未完成订单（暂停不撤单，先自行撤单）", open))
	}
	return out, nil
}

// liveAccounts 列出启用的实盘账户 ID。
func liveAccounts(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `SELECT c_trading_account_id FROM t_trading_accounts WHERE c_execution_mode = 'LIVE' AND c_status = 'ENABLED' ORDER BY c_trading_account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// tradeResetSpec 构造 Trade 的重置实现；syncAccount 向交易所对账一个账户（执行时的第一步用它重新核验）。
func tradeResetSpec(tradeStore *store.Store, dbPath string, syncAccount func(ctx context.Context, accountID string) error) *lifecycle.ResetSpec {
	dir := filepath.Dir(dbPath)
	return &lifecycle.ResetSpec{Plan: func(ctx context.Context, scope lifecycle.ResetScope) (lifecycle.ResetPlanInfo, []lifecycle.ResetStep, error) {
		sqlDB, err := tradeStore.SQLDB()
		if err != nil {
			return lifecycle.ResetPlanInfo{}, nil, err
		}
		lifecycle.RegisterSQLiteFile(sqlDB, dbPath)
		actual, err := lifecycle.BusinessTables(ctx, sqlDB)
		if err != nil {
			return lifecycle.ResetPlanInfo{}, nil, err
		}
		info := lifecycle.ResetPlanInfo{
			Keeps: []string{"数据库表结构", "交易所 API 密钥（在 Admin 的 secrets 里）", "发布内的配置文件", "生命周期状态文件"},
			Notes: []string{"只清本地账本，交易所上的仓位和订单不会因此消失"},
		}
		if unclassified, _ := lifecycle.ClassifyTables(actual, tradeDataTables, tradeConfigTables); len(unclassified) > 0 {
			info.Blockers = append(info.Blockers, "有未归类的表，先更新重置清单："+lifecycle.JoinTables(unclassified))
		}
		exposure, err := liveExposure(ctx, sqlDB)
		if err != nil {
			return lifecycle.ResetPlanInfo{}, nil, err
		}
		info.Blockers = append(info.Blockers, exposure...)

		tables := append([]string(nil), tradeDataTables...)
		if scope == lifecycle.ResetAll {
			tables = append(tables, tradeConfigTables...)
		} else {
			info.Keeps = append(info.Keeps, "交易账户、逻辑账户及成员、纸面账户配置、行情标的（配置）")
		}
		if rows, err := lifecycle.TableRows(ctx, sqlDB, tables); err == nil {
			info.Stats = map[string]any{"rows": rows}
		}
		steps := []lifecycle.ResetStep{
			{Name: "verify_no_live_exposure", Describe: "向交易所重新对账并核验实盘账户没有持仓和未完成订单", Run: func(ctx context.Context, _ string) error {
				if syncAccount != nil {
					ids, err := liveAccounts(ctx, sqlDB)
					if err != nil {
						return err
					}
					for _, id := range ids {
						if err := syncAccount(ctx, id); err != nil {
							return fmt.Errorf("向交易所对账实盘账户 %s 失败，无法确认没有持仓：%w", id, err)
						}
					}
				}
				exposure, err := liveExposure(ctx, sqlDB)
				if err != nil {
					return err
				}
				if len(exposure) > 0 {
					return fmt.Errorf("拒绝重置：%v", exposure)
				}
				return nil
			}},
			{Name: "backup", Describe: "备份 Trade 数据库到 reset-backups（保留最近 3 份）", Run: func(ctx context.Context, opID string) error {
				backupDir, err := lifecycle.ResetBackupDir(dir, opID, 3)
				if err != nil {
					return err
				}
				path, err := lifecycle.SQLiteBackup(ctx, sqlDB, backupDir, filepath.Base(dbPath))
				if err == nil {
					log.Infof("trade_reset_backup path=%s", path)
				}
				return err
			}},
			{Name: "clear_tables", Describe: "清空表：" + lifecycle.JoinTables(tables), Run: func(ctx context.Context, _ string) error {
				deleted, err := lifecycle.ClearTables(ctx, sqlDB, tables)
				if err == nil {
					log.Infof("trade_reset_cleared rows=%v", deleted)
				}
				return err
			}},
		}
		return info, steps, nil
	}}
}
