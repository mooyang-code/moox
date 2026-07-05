package rpc

import (
	"context"
	"strconv"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/trade/internal/config"
	"github.com/mooyang-code/moox/modules/trade/internal/service"
	"trpc.group/trpc-go/trpc-go/log"
)

var (
	defaultSyncServiceMu sync.RWMutex
	defaultSyncService   *service.Service
	syncRunMu            sync.Mutex
)

// SetDefaultSyncService 注入 timer handler 使用的 trade service。
func SetDefaultSyncService(svc *service.Service) {
	defaultSyncServiceMu.Lock()
	defer defaultSyncServiceMu.Unlock()
	defaultSyncService = svc
}

// HandleSyncSchedule 是 tRPC timer 回调入口。
func HandleSyncSchedule(ctx context.Context, params string) error {
	defaultSyncServiceMu.RLock()
	svc := defaultSyncService
	defaultSyncServiceMu.RUnlock()
	if svc == nil {
		log.WarnContext(ctx, "[TradeSync] default sync service is nil, skip")
		return nil
	}
	if !syncRunMu.TryLock() {
		log.WarnContext(ctx, "[TradeSync] previous sync still running, skip")
		return nil
	}
	defer syncRunMu.Unlock()

	opts := applySyncConfigDefaults(parseSyncScheduleParams(params))
	if opts.SpaceID == "" {
		opts.SpaceID = "crypto"
	}
	cfg := config.GetGlobalConfig()
	if cfg != nil && !cfg.Sync.Enabled {
		log.InfoContext(ctx, "[TradeSync] sync disabled by config, skip")
		return nil
	}
	if _, err := svc.SyncAllSnapshots(ctx, opts); err != nil {
		log.ErrorContextf(ctx, "[TradeSync] snapshot sync failed: %v", err)
	}
	if _, err := svc.SyncTradingHistory(ctx, opts); err != nil {
		log.ErrorContextf(ctx, "[TradeSync] history sync failed: %v", err)
	}
	return nil
}

func applySyncConfigDefaults(opts service.SyncOptions) service.SyncOptions {
	cfg := config.GetGlobalConfig()
	if cfg == nil {
		if opts.PageSize == 0 {
			opts.PageSize = 500
		}
		return opts
	}
	if opts.WindowHours == 0 {
		opts.WindowHours = cfg.Sync.WindowHours
	}
	if opts.PageSize == 0 {
		opts.PageSize = cfg.Sync.PageSize
	}
	if opts.MaxSymbolsPerRun == 0 {
		opts.MaxSymbolsPerRun = cfg.Sync.MaxSymbolsPerRun
	}
	if len(opts.Sections) == 0 {
		opts.Sections = map[service.SyncType]bool{
			service.SyncTypeBalances:  cfg.Sync.SyncBalances,
			service.SyncTypePositions: cfg.Sync.SyncPositions,
			service.SyncTypeOrders:    cfg.Sync.SyncOrders,
			service.SyncTypeTrades:    cfg.Sync.SyncTrades,
		}
	}
	return opts
}

func parseSyncScheduleParams(params string) service.SyncOptions {
	opts := service.SyncOptions{Sections: map[service.SyncType]bool{}}
	for _, part := range strings.Split(params, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			if opts.SpaceID == "" {
				opts.SpaceID = part
			}
			continue
		}
		key := strings.TrimSpace(kv[0])
		value := strings.TrimSpace(kv[1])
		switch key {
		case "space_id":
			opts.SpaceID = value
		case "account_id":
			opts.AccountID = value
		case "window_hours":
			opts.WindowHours, _ = strconv.Atoi(value)
		case "page_size":
			opts.PageSize, _ = strconv.Atoi(value)
		case "max_symbols", "max_symbols_per_run":
			opts.MaxSymbolsPerRun, _ = strconv.Atoi(value)
		case "sections":
			parseSyncSections(value, opts.Sections)
		}
	}
	return opts
}

func parseSyncSections(value string, out map[service.SyncType]bool) {
	for _, section := range strings.Split(value, ",") {
		switch strings.TrimSpace(section) {
		case "balances":
			out[service.SyncTypeBalances] = true
		case "positions":
			out[service.SyncTypePositions] = true
		case "orders":
			out[service.SyncTypeOrders] = true
		case "trades":
			out[service.SyncTypeTrades] = true
		}
	}
}
