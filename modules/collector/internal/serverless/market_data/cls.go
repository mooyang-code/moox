package marketdata

import (
	"context"
	"os"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/marketfetch"
	"github.com/mooyang-code/moox/packages/clsreporter"
	"trpc.group/trpc-go/trpc-go/log"
)

// newItemReporter 按 SCF 环境变量（MOOX_CLS_*）创建 CLS 上报器；未开启或配置不完整时返回 nil，采集照常进行。
func newItemReporter(ctx context.Context, functionName string) marketfetch.ItemReporter {
	cfg, enabled, err := clsreporter.ConfigFromEnv(os.Getenv)
	if err != nil {
		log.WarnContextf(ctx, "market_data_cls_config_invalid error=%v", err)
		return nil
	}
	if !enabled {
		return nil
	}
	if strings.TrimSpace(cfg.Source) == "" {
		cfg.Source = functionName
	}
	reporter, err := clsreporter.New(cfg)
	if err != nil {
		log.WarnContextf(ctx, "market_data_cls_setup_failed error=%v", err)
		return nil
	}
	return reporter
}
