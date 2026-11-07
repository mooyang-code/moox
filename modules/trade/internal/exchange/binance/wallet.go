package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange"
	"github.com/mooyang-code/moox/modules/trade/internal/exchange/httpclient"
	"github.com/mooyang-code/moox/modules/trade/internal/execution"
)

// 万向划转的类型：普通账户的现货（MAIN）和 U 本位合约（UMFUTURE）两个钱包之间。
const (
	transferToFutures = "MAIN_UMFUTURE"
	transferToSpot    = "UMFUTURE_MAIN"
)

// transferHistoryPageSize 是划转记录单页条数（官方上限 100）。
const transferHistoryPageSize = 100

// sapi 接口在现货域名上，统一账户和普通账户用同一套；签名与其他私有接口一致。
func (a *Adapter) sapiRequest(ctx context.Context, method, path string, values url.Values) ([]byte, error) {
	raw, err := a.spot.Do(ctx, &httpclient.Request{
		Method: method, Path: path, RawQuery: a.signedQuery(values),
		Headers: map[string]string{"X-MBX-APIKEY": a.credential.APIKey},
	})
	return raw, classifyAPIError(err, raw)
}

// Supports 说明可选能力在这个适配器上是否可用：币安普通账户按市场拆成现货、合约两个适配器，
// 划转和小额兑换只放在现货适配器上，BNB 抵扣两个各设自己负责的那部分。
func (a *Adapter) Supports(capability string) bool {
	switch capability {
	case execution.CapabilityTransfer:
		return a.papi == nil && a.config.Market() == exchange.MarketTypeSpot
	case execution.CapabilityFeeDiscount:
		return true
	case execution.CapabilityDust:
		return a.config.Market() == exchange.MarketTypeSpot
	}
	return false
}

// Transfer 在普通账户的现货、合约钱包之间划转（POST /sapi/v1/asset/transfer）。
// 官方接口没有客户端流水号，clientRef 不发给交易所；超时后靠 ListTransfers 按时间窗口和金额对账。
func (a *Adapter) Transfer(ctx context.Context, from, to exchange.Wallet, asset string, amount shared.Decimal, clientRef string) (execution.TransferReceipt, error) {
	kind, err := transferType(from, to)
	if err != nil {
		return execution.TransferReceipt{}, err
	}
	if strings.TrimSpace(asset) == "" || amount.Cmp(shared.Zero()) <= 0 {
		return execution.TransferReceipt{}, typedRejected("划转需要资产和大于 0 的金额", nil)
	}
	raw, err := a.sapiRequest(ctx, http.MethodPost, "/sapi/v1/asset/transfer", url.Values{
		"type": []string{kind}, "asset": []string{asset}, "amount": []string{amount.String()},
	})
	if err != nil {
		return execution.TransferReceipt{}, err
	}
	var payload struct {
		TranID json.Number `json:"tranId"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil || payload.TranID.String() == "" {
		// 请求已发出但回执解析不了：结果未知，由调用方对账。
		return execution.TransferReceipt{}, transportUnknown("decode transfer receipt", err)
	}
	return execution.TransferReceipt{ExchangeTransferID: payload.TranID.String()}, nil
}

func transferType(from, to exchange.Wallet) (string, error) {
	switch {
	case from == exchange.WalletSpot && to == exchange.WalletFutures:
		return transferToFutures, nil
	case from == exchange.WalletFutures && to == exchange.WalletSpot:
		return transferToSpot, nil
	}
	return "", typedRejected(fmt.Sprintf("不支持从 %s 钱包划转到 %s 钱包", from, to), nil)
}

type transferHistory struct {
	Total int `json:"total"`
	Rows  []struct {
		Asset     string      `json:"asset"`
		Amount    string      `json:"amount"`
		Type      string      `json:"type"`
		Status    string      `json:"status"`
		TranID    json.Number `json:"tranId"`
		Timestamp int64       `json:"timestamp"`
	} `json:"rows"`
}

// ListTransfers 查询 since 之后两个方向的划转记录（GET /sapi/v1/asset/transfer，一页 100 条，翻页取完）。
// 记录里的状态 PENDING 不等于没到账：对账时要等它变成 CONFIRMED 或 FAILED 再下结论。
func (a *Adapter) ListTransfers(ctx context.Context, since time.Time) ([]execution.TransferRecord, error) {
	var records []execution.TransferRecord
	for _, kind := range []string{transferToFutures, transferToSpot} {
		for page := 1; ; page++ {
			raw, err := a.sapiRequest(ctx, http.MethodGet, "/sapi/v1/asset/transfer", url.Values{
				"type": []string{kind}, "startTime": []string{strconv.FormatInt(since.UnixMilli(), 10)},
				"current": []string{strconv.Itoa(page)}, "size": []string{strconv.Itoa(transferHistoryPageSize)},
			})
			if err != nil {
				return nil, err
			}
			var payload transferHistory
			if err := json.Unmarshal(raw, &payload); err != nil {
				return nil, typedRejected("decode transfer history", err)
			}
			for _, row := range payload.Rows {
				amount, err := decimalOrZero(row.Amount)
				if err != nil {
					return nil, err
				}
				from, to := exchange.WalletSpot, exchange.WalletFutures
				if row.Type == transferToSpot {
					from, to = exchange.WalletFutures, exchange.WalletSpot
				}
				records = append(records, execution.TransferRecord{
					ExchangeTransferID: row.TranID.String(), Asset: row.Asset, Amount: amount, From: from, To: to,
					Status: transferStatus(row.Status), At: millis(row.Timestamp),
				})
			}
			if len(payload.Rows) < transferHistoryPageSize || page*transferHistoryPageSize >= payload.Total {
				break
			}
		}
	}
	return records, nil
}

func transferStatus(raw string) execution.TransferStatus {
	switch raw {
	case "CONFIRMED":
		return execution.TransferConfirmed
	case "FAILED":
		return execution.TransferFailed
	}
	return execution.TransferPending
}

// SetBNBFeeDiscount 开关 BNB 抵扣：现货适配器设现货交易抵扣（POST /sapi/v1/bnbBurn，统一账户同时设借币利息抵扣），
// 合约适配器设合约抵扣（普通账户 POST /fapi/v1/feeBurn，统一账户 POST /papi/v1/um/feeBurn）。
func (a *Adapter) SetBNBFeeDiscount(ctx context.Context, enabled bool) error {
	value := strconv.FormatBool(enabled)
	if a.config.Market() == exchange.MarketTypeSwap {
		raw, err := a.request(ctx, http.MethodPost, a.feeBurnPath(), url.Values{"feeBurn": []string{value}})
		return classifyAPIError(err, raw)
	}
	values := url.Values{"spotBNBBurn": []string{value}}
	if a.papi != nil {
		values.Set("interestBNBBurn", value)
	}
	_, err := a.sapiRequest(ctx, http.MethodPost, "/sapi/v1/bnbBurn", values)
	return err
}

// GetBNBFeeDiscount 报告这个适配器负责的那部分抵扣是否开启。
func (a *Adapter) GetBNBFeeDiscount(ctx context.Context) (bool, error) {
	if a.config.Market() == exchange.MarketTypeSwap {
		raw, err := a.request(ctx, http.MethodGet, a.feeBurnPath(), nil)
		if err != nil {
			return false, classifyAPIError(err, raw)
		}
		var payload struct {
			FeeBurn bool `json:"feeBurn"`
		}
		if err := json.Unmarshal(raw, &payload); err != nil {
			return false, typedRejected("decode fee burn status", err)
		}
		return payload.FeeBurn, nil
	}
	raw, err := a.sapiRequest(ctx, http.MethodGet, "/sapi/v1/bnbBurn", nil)
	if err != nil {
		return false, err
	}
	var payload struct {
		SpotBNBBurn     bool `json:"spotBNBBurn"`
		InterestBNBBurn bool `json:"interestBNBBurn"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return false, typedRejected("decode BNB burn status", err)
	}
	if a.papi != nil {
		return payload.SpotBNBBurn && payload.InterestBNBBurn, nil
	}
	return payload.SpotBNBBurn, nil
}

func (a *Adapter) feeBurnPath() string {
	if a.papi != nil {
		return "/papi/v1/um/feeBurn"
	}
	return "/fapi/v1/feeBurn"
}

// dustAccountType 是小额兑换的账户类型：普通账户的现货钱包 SPOT，统一账户的杠杆钱包 MARGIN。
func (a *Adapter) dustAccountType() string {
	if a.papi != nil {
		return "MARGIN"
	}
	return "SPOT"
}

// ListDust 列出可以兑换成 BNB 的小额资产（POST /sapi/v1/asset/dust-btc）。只有现货适配器有这个能力。
func (a *Adapter) ListDust(ctx context.Context) ([]execution.DustAsset, error) {
	if a.config.Market() != exchange.MarketTypeSpot {
		return nil, nil
	}
	raw, err := a.sapiRequest(ctx, http.MethodPost, "/sapi/v1/asset/dust-btc", url.Values{"accountType": []string{a.dustAccountType()}})
	if err != nil {
		return nil, err
	}
	var payload struct {
		Details []struct {
			Asset      string `json:"asset"`
			AmountFree string `json:"amountFree"`
			ToBNB      string `json:"toBNB"`
		} `json:"details"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, typedRejected("decode dust list", err)
	}
	out := make([]execution.DustAsset, 0, len(payload.Details))
	for _, row := range payload.Details {
		amount, err := decimalOrZero(row.AmountFree)
		if err != nil {
			return nil, err
		}
		bnb, err := decimalOrZero(row.ToBNB)
		if err != nil {
			return nil, err
		}
		out = append(out, execution.DustAsset{Asset: row.Asset, Amount: amount, BNBValue: bnb})
	}
	return out, nil
}

// ConvertDust 把指定的小额资产兑换成 BNB（POST /sapi/v1/asset/dust，asset 重复传多次）。
func (a *Adapter) ConvertDust(ctx context.Context, assets []string) (execution.DustResult, error) {
	if a.config.Market() != exchange.MarketTypeSpot || len(assets) == 0 {
		return execution.DustResult{}, nil
	}
	values := url.Values{"accountType": []string{a.dustAccountType()}, "asset": append([]string(nil), assets...)}
	raw, err := a.sapiRequest(ctx, http.MethodPost, "/sapi/v1/asset/dust", values)
	if err != nil {
		return execution.DustResult{}, err
	}
	var payload struct {
		TotalTransfered string `json:"totalTransfered"`
		TransferResult  []struct {
			FromAsset string      `json:"fromAsset"`
			TranID    json.Number `json:"tranId"`
		} `json:"transferResult"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return execution.DustResult{}, typedRejected("decode dust conversion", err)
	}
	total, err := decimalOrZero(payload.TotalTransfered)
	if err != nil {
		return execution.DustResult{}, err
	}
	result := execution.DustResult{TotalBNB: total}
	for _, row := range payload.TransferResult {
		result.ConvertedList = append(result.ConvertedList, row.FromAsset)
		result.TransferIDs = append(result.TransferIDs, row.TranID.String())
	}
	return result, nil
}
