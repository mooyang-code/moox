package marketfetch

import (
	"os"
	"strconv"
	"strings"
	"time"
)

// The composition root consumes the same validated route configuration as the
// assignment planner, without placing concrete provider constructors here.
type StockCNRoute = stockCNRoute
type StockCNProviderRuntime = stockCNProviderRuntime
type StockCNProviderKlineConfig = stockCNProviderKlineFile

const KlineProviderAttemptBudget = klineProviderAttemptBudget

// RuntimeKlineProviderAttemptBudget reads the SCF-owned HTTP attempt budget.
// The static constant remains the local fallback and the retry cursor unit.
func RuntimeKlineProviderAttemptBudget() int {
	raw := strings.TrimSpace(os.Getenv("MOOX_FETCH_HTTP_MAX_ATTEMPTS"))
	if raw == "" {
		return klineProviderAttemptBudget
	}
	attempts, err := strconv.Atoi(raw)
	if err != nil || attempts <= 0 {
		return klineProviderAttemptBudget
	}
	return attempts
}

// RuntimeKlineBreakerThreshold 是单次调用内同一行情源的连续失败阈值，同时也是该行情源允许的在途请求数上限。
// 阈值 = 每个行情源的尝试次数 × 并发度（计划下发的 MOOX_FETCH_FEED_CONCURRENCY，未设置按 1）：
// 单行情源（crypto）没有备用源可切换，阈值过小时，行情源在整刻度出现延迟尖峰，头几个请求超时就会让整批其余请求直接失败，
// 且在途请求被压到只有几个，慢响应会把整批拖过窗口。
func RuntimeKlineBreakerThreshold() int {
	return RuntimeKlineProviderAttemptBudget() * max(1, envInt("MOOX_FETCH_FEED_CONCURRENCY", 1))
}

func LoadStockCNRoute() (StockCNRoute, error) { return loadStockCNRoute() }

func LoadStockCNProviderRuntime(route StockCNRoute) (map[string]StockCNProviderRuntime, error) {
	return loadStockCNProviderRuntime(route)
}

// attemptTimeoutFloor 是单次行情请求的最短超时，来自计划下发的 MOOX_FETCH_REQUEST_TIMEOUT_MS；未配置时为 0（不设下限）。
func attemptTimeoutFloor() time.Duration {
	millis := envInt("MOOX_FETCH_REQUEST_TIMEOUT_MS", 0)
	if millis <= 0 {
		return 0
	}
	return time.Duration(millis) * time.Millisecond
}
