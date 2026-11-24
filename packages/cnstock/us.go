package cnstock

import (
	"bytes"
	"context"
	"fmt"
	"strings"
)

// 腾讯美股实时快照 qt.gtimg.cn/q=usAAPL,usMSFT,... 返回 GBK 文本，每只一行：v_usAAPL="200~苹果~AAPL.OQ~...";
// 字段用 ~ 分隔，共 73 个。这里只用第 40 个字段（交易状态），与 A 股一致：空为正常，S 为停牌。
// 2026-11-24 用纳斯达克交易暂停列表里仍处于暂停的股票核对：纳斯达克上市的 IPFX、TJGC、HCHL（暂停原因 T12）为 S、当天量 0；
// AMEX 上市的 DDC、TOVX（暂停原因 H11）为空，没有标出；正常交易的 AAPL 为空。D 按 A 股的约定当退市处理，没有样例核对。
const (
	usTencentPrefix = "us"
	usFieldStatus   = 40
	usMinFields     = 41
)

// USTencentSymbol 把美股标的 ID 换成腾讯代码：AAPL.XNAS → usAAPL，BRK.B.XNYS → usBRK.B（去掉最后一段交易所 MIC）。
func USTencentSymbol(instrumentID string) (string, error) {
	instrumentID = strings.TrimSpace(instrumentID)
	cut := strings.LastIndex(instrumentID, ".")
	if cut <= 0 || cut == len(instrumentID)-1 {
		return "", fmt.Errorf("%w：美股标的 %q", ErrInvalidCode, instrumentID)
	}
	return usTencentPrefix + strings.ToUpper(instrumentID[:cut]), nil
}

// StatusUS 取一批美股的交易状态（只填 InstrumentID、Halted、Delisted），超过 TencentBatchLimit 只时分批请求；
// 任一批请求失败就整体返回错误，需要逐批处理的调用方自己分批。上游不认识的代码不在结果里。
func (c TencentClient) StatusUS(ctx context.Context, instrumentIDs []string) (map[string]Quote, error) {
	result := make(map[string]Quote, len(instrumentIDs))
	for start := 0; start < len(instrumentIDs); start += TencentBatchLimit {
		end := min(start+TencentBatchLimit, len(instrumentIDs))
		bySymbol := make(map[string]string, end-start)
		symbols := make([]string, 0, end-start)
		for _, instrumentID := range instrumentIDs[start:end] {
			symbol, err := USTencentSymbol(instrumentID)
			if err != nil {
				return nil, err
			}
			bySymbol[strings.ToLower(symbol)] = instrumentID
			symbols = append(symbols, symbol)
		}
		body, err := c.fetch(ctx, symbols)
		if err != nil {
			return nil, err
		}
		for instrumentID, quote := range ParseTencentUSStatus(body, bySymbol) {
			result[instrumentID] = quote
		}
	}
	return result, nil
}

// ParseTencentUSStatus 解析美股快照里的交易状态。bySymbol 是请求时腾讯代码（小写）到标的 ID 的对照；不在对照里的行、
// 格式不对的行跳过。
func ParseTencentUSStatus(body []byte, bySymbol map[string]string) map[string]Quote {
	out := map[string]Quote{}
	for _, line := range bytes.Split(body, []byte(";")) {
		line = bytes.TrimSpace(line)
		name, value, ok := bytes.Cut(line, []byte("="))
		if !ok || !bytes.HasPrefix(name, []byte("v_")) {
			continue
		}
		instrumentID, known := bySymbol[strings.ToLower(string(bytes.TrimPrefix(name, []byte("v_"))))]
		value = bytes.Trim(value, `"`)
		if !known || len(value) <= 2 {
			continue
		}
		fields := splitGBK(value, '~')
		if len(fields) < usMinFields {
			continue
		}
		status := strings.TrimSpace(fields[usFieldStatus])
		out[instrumentID] = Quote{
			InstrumentID: instrumentID,
			Halted:       status == tencentStatusHalted,
			Delisted:     status == tencentStatusDelisted,
		}
	}
	return out
}
