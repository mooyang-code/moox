package binance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
)

const (
	productionPapiURL = "https://papi.binance.com"
	// 统一账户接口每分钟 IP 权重额度。未核对：官方 SDK 只给了各接口的权重（balance、account 为 20 等），
	// 没有找到整体额度的原文，这里按 6000 估计，只用来计算用量比例；真实限频以响应头 X-MBX-USED-WEIGHT-1M 和 -1003 为准。
	papiWeightLimit = 6000
)

// authErrorCodes 是只表示“这把密钥不能这样用”的币安错误码，只有它们能参与账户类型和权限的判别：
//
//	-2015 密钥、IP 或权限无效（官方文档原文：Invalid API-key, IP, or permissions for action）；
//	-2014 密钥格式无效；-2008 密钥 ID 无效；-1002 未授权；-1022 签名无效。
//
// 其余错误码（限频 -1003、超时 -1007、时间戳 -1021 等）说明这次没检测成，不下结论。
// 未核对：官方文档没有写明开通统一账户后 fapi、以及普通账户调用 papi 具体返回哪个码，
// 这里按社区报告都是 -2015 处理；遇到其他码保守地按“没有结论”，不会误判账户类型。
var authErrorCodes = map[string]bool{
	"-2015": true, "-2014": true, "-2008": true, "-1002": true, "-1022": true,
}

// inspectClients 是检测要用的三个域名：sapi 在现货域名上。
type inspectClients struct {
	spot, swap, papi *httpclient.Client
}

func newInspectClients() inspectClients {
	spot, swap, papi := httpclient.New(productionSpotURL), httpclient.New(productionSwapURL), httpclient.New(productionPapiURL)
	for _, client := range []*httpclient.Client{spot, swap, papi} {
		client.Classify = classifyRateLimit
	}
	papi.SetWeightLimit(papiWeightLimit)
	return inspectClients{spot: spot, swap: swap, papi: papi}
}

// Inspect 检测币安密钥：权限、账户类型（统一账户或普通账户）、合约持仓模式。
// 返回错误表示这次没有检测成功，不代表账户有问题（见 exchange.Inspector）。
func Inspect(ctx context.Context, request exchange.InspectRequest) (exchange.Inspection, error) {
	return inspectWith(ctx, request, newInspectClients())
}

// apiRestrictions 是 GET /sapi/v1/account/apiRestrictions 的响应（官方样例见 testdata/api_restrictions.json）。
type apiRestrictions struct {
	IPRestrict                   bool `json:"ipRestrict"`
	EnableReading                bool `json:"enableReading"`
	EnableWithdrawals            bool `json:"enableWithdrawals"`
	EnableFutures                bool `json:"enableFutures"`
	PermitsUniversalTransfer     bool `json:"permitsUniversalTransfer"`
	EnableSpotAndMarginTrading   bool `json:"enableSpotAndMarginTrading"`
	EnablePortfolioMarginTrading bool `json:"enablePortfolioMarginTrading"`
}

func inspectWith(ctx context.Context, request exchange.InspectRequest, clients inspectClients) (exchange.Inspection, error) {
	// 先测时钟：/api/v3/time 不签名。本机时钟偏快 1 秒以上时签名请求全被 -1021 拒绝，签名检测得不出结论，
	// 要是不先测，用户只会看到“检测没有完成”，看不到真正的原因。测试网跳过检测，也不测。
	var skew int64
	if request.Environment != exchange.AccountEnvironmentTestnet {
		skew = measureClockSkew(ctx, clients)
	}
	result, err := inspectAccount(ctx, request, clients)
	if err != nil {
		if errors.Is(err, exchange.ErrInspectionInconclusive) && time.Duration(abs(skew))*time.Millisecond >= exchange.ClockSkewBlock {
			// 签名检测没有结论，而时钟偏差已经足以解释：把时钟偏差作为结论返回，让用户看到真正的原因。
			result = exchange.Inspection{
				Exchange: exchange.ExchangeBinance, Environment: request.Environment,
				InspectedAt: time.Now().UTC(), ClockSkewMillis: skew,
			}
			result.ApplyFindings(request.Markets)
			return result, nil
		}
		return result, err
	}
	if result.Skipped || result.CredentialRejected != "" {
		return result, nil
	}
	result.ClockSkewMillis = skew
	result.ApplyFindings(request.Markets)
	return result, nil
}

// measureClockSkew 取现货 /api/v3/time 和合约 /fapi/v1/time 各测一次本机时钟偏差，从测到的有效样本里
// 返回绝对值较大的一个（毫秒）。取不到交易所时间、往返太慢的样本都不算；没有有效样本时按没有偏差（0）处理。
func measureClockSkew(ctx context.Context, clients inspectClients) int64 {
	var worst int64
	for _, probe := range []struct {
		client *httpclient.Client
		path   string
	}{{clients.spot, "/api/v3/time"}, {clients.swap, "/fapi/v1/time"}} {
		sentAt := time.Now()
		raw, err := probe.client.Do(ctx, &httpclient.Request{Method: http.MethodGet, Path: probe.path, Timeout: 2 * time.Second})
		receivedAt := time.Now()
		var payload struct {
			ServerTime int64 `json:"serverTime"`
		}
		if err != nil || json.Unmarshal(raw, &payload) != nil || payload.ServerTime <= 0 {
			continue
		}
		measured, ok := exchange.MeasureClockSkew(sentAt, receivedAt, time.UnixMilli(payload.ServerTime))
		if !ok {
			continue
		}
		if skew := measured.Milliseconds(); abs(skew) > abs(worst) {
			worst = skew
		}
	}
	return worst
}

func abs(value int64) int64 {
	if value < 0 {
		return -value
	}
	return value
}

func inspectAccount(ctx context.Context, request exchange.InspectRequest, clients inspectClients) (exchange.Inspection, error) {
	result := exchange.Inspection{
		Exchange: exchange.ExchangeBinance, Environment: request.Environment, InspectedAt: time.Now().UTC(),
	}
	if request.Environment == exchange.AccountEnvironmentTestnet {
		// 币安测试网没有 sapi 和 papi，现货和合约是两套域名和两把密钥：跳过权限与类型检测，一个测试网账户只开一个市场。
		result.Skipped = true
		result.AccountMode = exchange.AccountModeBinanceStandard
		result.CanTradeSpot, result.CanTradeSwap = true, true
		result.AvailableMarkets = []exchange.MarketType{exchange.MarketTypeSpot, exchange.MarketTypeSwap}
		return result, nil
	}
	credential := request.Credential
	get := func(client *httpclient.Client, path string) ([]byte, error) {
		return client.Do(ctx, &httpclient.Request{
			Method: http.MethodGet, Path: path,
			RawQuery: signQuery(credential.APISecret, url.Values{}),
			Headers:  map[string]string{"X-MBX-APIKEY": credential.APIKey},
		})
	}

	// 1. 权限。
	raw, err := get(clients.spot, "/sapi/v1/account/apiRestrictions")
	if err != nil {
		reason, inconclusive := inspectionFailure(err, raw)
		if inconclusive != nil {
			return exchange.Inspection{}, inconclusive
		}
		result.CredentialRejected = reason
		return finishInspection(result, request), nil
	}
	var permissions apiRestrictions
	if err := json.Unmarshal(raw, &permissions); err != nil {
		return exchange.Inspection{}, inconclusiveError(fmt.Errorf("解析 apiRestrictions：%w", err))
	}
	result.CanWithdraw = permissions.EnableWithdrawals
	result.UniversalTransfer = permissions.PermitsUniversalTransfer
	result.IPRestricted = permissions.IPRestrict
	if !permissions.EnableReading {
		result.CredentialRejected = "密钥没有读取权限"
		return finishInspection(result, request), nil
	}

	// 2. 统一账户：papi 能调通就是。
	raw, err = get(clients.papi, "/papi/v1/account")
	if err == nil {
		result.AccountMode = exchange.AccountModeBinancePortfolioMargin
		// 统一账户的交易权限由 enablePortfolioMarginTrading 控制（官方 apiRestrictions 样例里它为 true、
		// enableSpotAndMarginTrading 和 enableFutures 为 false）。
		result.CanTradeSpot, result.CanTradeSwap = permissions.EnablePortfolioMarginTrading, permissions.EnablePortfolioMarginTrading
		mode, err := positionMode(ctx, clients.papi, "/papi/v1/um/positionSide/dual", credential)
		if err != nil {
			return exchange.Inspection{}, err
		}
		result.PositionMode = mode
		return finishInspection(result, request), nil
	}
	// papi 被鉴权类错误拒绝：不是统一账户，或密钥建于开通之前，继续看 fapi；其余错误没有结论。
	if _, inconclusive := inspectionFailure(err, raw); inconclusive != nil {
		return exchange.Inspection{}, inconclusive
	}

	// 3. 普通账户：fapi 能调通就是；两边都被鉴权类错误拒绝且密钥开了合约权限，疑似统一账户的旧密钥。
	result.AccountMode = exchange.AccountModeBinanceStandard
	result.CanTradeSpot = permissions.EnableSpotAndMarginTrading
	raw, err = get(clients.swap, "/fapi/v3/account")
	switch {
	case err == nil:
		result.CanTradeSwap = permissions.EnableFutures
		if permissions.EnableFutures {
			mode, err := positionMode(ctx, clients.swap, "/fapi/v1/positionSide/dual", credential)
			if err != nil {
				return exchange.Inspection{}, err
			}
			result.PositionMode = mode
			multi, err := multiAssetsMargin(ctx, clients.swap, credential)
			if err != nil {
				return exchange.Inspection{}, err
			}
			result.MultiAssetsMargin = multi
		}
	default:
		if _, inconclusive := inspectionFailure(err, raw); inconclusive != nil {
			return exchange.Inspection{}, inconclusive
		}
		if permissions.EnableFutures {
			result.CanTradeSwap = true
			result.PortfolioMarginSuspected = true
		}
	}
	return finishInspection(result, request), nil
}

// finishInspection 补全能开通的市场和按请求市场算出的阻断项。
func finishInspection(result exchange.Inspection, request exchange.InspectRequest) exchange.Inspection {
	if result.CanTradeSpot {
		result.AvailableMarkets = append(result.AvailableMarkets, exchange.MarketTypeSpot)
	}
	if result.CanTradeSwap && !result.PortfolioMarginSuspected {
		result.AvailableMarkets = append(result.AvailableMarkets, exchange.MarketTypeSwap)
	}
	result.ApplyFindings(request.Markets)
	return result
}

// positionMode 读取合约持仓模式。鉴权类错误按“读不到”处理（密钥没有合约权限），其余错误没有结论。
func positionMode(ctx context.Context, client *httpclient.Client, path string, credential exchange.Credential) (exchange.PositionMode, error) {
	raw, err := client.Do(ctx, &httpclient.Request{
		Method: http.MethodGet, Path: path,
		RawQuery: signQuery(credential.APISecret, url.Values{}),
		Headers:  map[string]string{"X-MBX-APIKEY": credential.APIKey},
	})
	if err != nil {
		if _, inconclusive := inspectionFailure(err, raw); inconclusive != nil {
			return exchange.PositionModeUnknown, inconclusive
		}
		return exchange.PositionModeUnknown, nil
	}
	var payload struct {
		DualSidePosition bool `json:"dualSidePosition"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return exchange.PositionModeUnknown, inconclusiveError(fmt.Errorf("解析持仓模式：%w", err))
	}
	if payload.DualSidePosition {
		return exchange.PositionModeHedge, nil
	}
	return exchange.PositionModeOneWay, nil
}

func multiAssetsMargin(ctx context.Context, client *httpclient.Client, credential exchange.Credential) (bool, error) {
	raw, err := client.Do(ctx, &httpclient.Request{
		Method: http.MethodGet, Path: "/fapi/v1/multiAssetsMargin",
		RawQuery: signQuery(credential.APISecret, url.Values{}),
		Headers:  map[string]string{"X-MBX-APIKEY": credential.APIKey},
	})
	if err != nil {
		if _, inconclusive := inspectionFailure(err, raw); inconclusive != nil {
			return false, inconclusive
		}
		return false, nil
	}
	var payload struct {
		MultiAssetsMargin bool `json:"multiAssetsMargin"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false, inconclusiveError(fmt.Errorf("解析多资产模式：%w", err))
	}
	return payload.MultiAssetsMargin, nil
}

// inspectionFailure 区分一次失败的调用：先看状态码和错误类别，再看错误码。
// 限频（429、-1003）、IP 封禁（418）、WAF（403）、5xx、超时、本地限流拦截都是“没有结论”：这类响应体里可能带着
// 鉴权类的错误码（网关、WAF 页面或限流时的错误体），不能据此判成“密钥被拒绝”。
// 只有状态码和类别都不是上面这些，响应体里又是鉴权、权限类错误码时才返回说明（确定的结论）；其余一律“没有结论”。
func inspectionFailure(err error, raw []byte) (reason string, inconclusive error) {
	if transientFailure(err) {
		return "", inconclusiveError(err)
	}
	code := apiCode(raw)
	if authErrorCodes[code] {
		return "交易所返回鉴权类错误 " + code + "（密钥无效、IP 不在白名单或没有对应权限）", nil
	}
	return "", inconclusiveError(err)
}

// transientFailure 报告错误是否一定不是“密钥被拒绝”的确定结论：限频、封禁、WAF、5xx、超时和传输错误、本地限流拦截。
func transientFailure(err error) bool {
	var typed *exchange.Error
	if !errors.As(err, &typed) {
		return true
	}
	switch typed.HTTPStatus {
	case http.StatusForbidden, http.StatusTeapot, http.StatusTooManyRequests:
		return true
	}
	if typed.HTTPStatus >= 500 {
		return true
	}
	switch typed.Kind {
	case exchange.ErrorRateLimited, exchange.ErrorOrderRateLimited, exchange.ErrorBanned, exchange.ErrorThrottled,
		exchange.ErrorNotSent, exchange.ErrorNotReady, exchange.ErrorTransportUnknown:
		return true
	}
	return false
}

func inconclusiveError(err error) error {
	return errors.Join(exchange.ErrInspectionInconclusive, err)
}
