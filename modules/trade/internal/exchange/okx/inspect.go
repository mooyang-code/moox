package okx

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
)

// Inspect 检测 OKX 密钥：权限（perm、绑定 IP）、账户层级（acctLv）、合约持仓模式（posMode）。
// 全部取自 GET /api/v5/account/config（官方样例见 testdata/account_config.json）。
// 返回错误表示这次没有检测成功（超时、限频、5xx），不代表账户有问题（见 exchange.Inspector）。
func Inspect(ctx context.Context, request exchange.InspectRequest) (exchange.Inspection, error) {
	adapter := New(exchange.AccountConfig{Exchange: exchange.ExchangeOKX, Environment: request.Environment}, request.Credential)
	return inspectWith(ctx, request, adapter)
}

// accountConfig 是账户配置里检测用到的字段。
type accountConfig struct {
	AcctLv  string `json:"acctLv"`
	Perm    string `json:"perm"`
	IP      string `json:"ip"`
	PosMode string `json:"posMode"`
}

func inspectWith(ctx context.Context, request exchange.InspectRequest, adapter *Adapter) (exchange.Inspection, error) {
	result, err := inspectAccount(ctx, request, adapter)
	if err != nil || result.CredentialRejected != "" {
		return result, err
	}
	result.ClockSkewMillis = measureClockSkew(ctx, adapter)
	result.ApplyFindings(request.Markets)
	return result, nil
}

// measureClockSkew 用 /api/v5/public/time 测一次本机时钟偏差（毫秒，交易所时间减本机时间）。
// 取不到交易所时间不影响账户检测，按没有偏差处理：其余检测调用同样会暴露网络问题。
func measureClockSkew(ctx context.Context, adapter *Adapter) int64 {
	sentAt := time.Now()
	data, err := adapter.request(ctx, http.MethodGet, "/api/v5/public/time", nil, nil, false)
	receivedAt := time.Now()
	var rows []struct {
		TS string `json:"ts"`
	}
	if err != nil || json.Unmarshal(data, &rows) != nil || len(rows) == 0 {
		return 0
	}
	serverTime := millisString(rows[0].TS)
	if serverTime.IsZero() {
		return 0
	}
	return exchange.MeasureClockSkew(sentAt, receivedAt, serverTime).Milliseconds()
}

func inspectAccount(ctx context.Context, request exchange.InspectRequest, adapter *Adapter) (exchange.Inspection, error) {
	result := exchange.Inspection{
		Exchange: exchange.ExchangeOKX, Environment: request.Environment,
		AccountMode: exchange.AccountModeOKXUnified, InspectedAt: time.Now().UTC(),
	}
	if strings.TrimSpace(request.Credential.Passphrase) == "" {
		result.CredentialRejected = "OKX 密钥缺少 passphrase"
		result.ApplyFindings(request.Markets)
		return result, nil
	}
	data, err := adapter.request(ctx, http.MethodGet, "/api/v5/account/config", nil, nil, true)
	if err != nil {
		// 只有鉴权类错误码是确定的结论；其余（超时、限频、5xx、无法识别的错误码）这次没检测成，不下结论。
		if isCredentialRejection(err) {
			// 只记错误码和中文说明，不带交易所原始响应体和请求串（可能含签名）。
			result.CredentialRejected = "交易所返回鉴权类错误 " + errorCode(err) + "（密钥、passphrase 无效或 IP 不在白名单）"
			result.ApplyFindings(request.Markets)
			return result, nil
		}
		return exchange.Inspection{}, errors.Join(exchange.ErrInspectionInconclusive, err)
	}
	var rows []accountConfig
	if err := json.Unmarshal(data, &rows); err != nil || len(rows) == 0 {
		return exchange.Inspection{}, errors.Join(exchange.ErrInspectionInconclusive, fmt.Errorf("解析账户配置：%v", err))
	}
	config := rows[0]
	permissions := map[string]bool{}
	for _, item := range strings.Split(config.Perm, ",") {
		permissions[strings.TrimSpace(item)] = true
	}
	result.CanWithdraw = permissions["withdraw"]
	result.CanTradeSpot = permissions["trade"]
	result.CanTradeSwap = permissions["trade"]
	result.IPRestricted = strings.TrimSpace(config.IP) != ""
	result.RawPermissions = "perm=" + config.Perm + "; ip=" + config.IP
	result.OKXAcctLv = config.AcctLv
	// acctLv：1 现货模式、2 合约模式、3 跨币种保证金、4 组合保证金。1 只能做现货。
	switch config.PosMode {
	case "net_mode":
		result.PositionMode = exchange.PositionModeOneWay
	case "long_short_mode":
		result.PositionMode = exchange.PositionModeHedge
	}
	if result.CanTradeSpot {
		result.AvailableMarkets = append(result.AvailableMarkets, exchange.MarketTypeSpot)
	}
	if result.CanTradeSwap && config.AcctLv != "1" {
		result.AvailableMarkets = append(result.AvailableMarkets, exchange.MarketTypeSwap)
	}
	result.ApplyFindings(request.Markets)
	return result, nil
}

// isCredentialRejection 报告错误是否是确定的“密钥被拒绝”：响应体里解析出了鉴权类错误码。
// 没有可识别的错误码（HTTP 401 配 HTML、空响应体等）、以及与时间戳有关的 50102（请求过期）、50112，
// 都不是确定结论：海外机时钟偏差、网关异常时不能判成“密钥无效”暂停组合，按没有结论处理。
// 50102、50112 的官方说明未逐字核对。
func isCredentialRejection(err error) bool {
	var typed *exchange.Error
	if !errors.As(err, &typed) || typed.Kind != exchange.ErrorAuthentication || typed.HTTPStatus >= 500 {
		return false
	}
	return typed.Code != "" && typed.Code != "50102" && typed.Code != "50112"
}

func errorCode(err error) string {
	var typed *exchange.Error
	if errors.As(err, &typed) {
		return typed.Code
	}
	return ""
}
