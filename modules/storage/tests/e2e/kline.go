//go:build e2e

package e2e

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/storage/internal/testutil"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

// Kline 是一条 K 线事实数据，对应 AR-USDT.csv 的一行。
type Kline struct {
	Time        time.Time
	Open        float64
	High        float64
	Low         float64
	Close       float64
	Volume      float64
	QuoteVolume float64
	TradeNum    int64
	Symbol      string
}

// csvTimeLayout 是 AR-USDT.csv 中 candle_begin_time 的时间格式。
const csvTimeLayout = "2006-01-02 15:04:05"

// klineCSVPath 返回测试 K 线 CSV 文件路径。
// 默认使用本机下载目录下的 AR-USDT.csv，可用环境变量 MOOX_E2E_KLINE_CSV 覆盖。
func klineCSVPath() string {
	if path := strings.TrimSpace(os.Getenv("MOOX_E2E_KLINE_CSV")); path != "" {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "AR-USDT.csv"
	}
	return filepath.Join(home, "Downloads", "AR-USDT.csv")
}

// klineRowLimit 返回最多载入多少行 K 线，避免一次写入 3 万行拖慢 e2e。
// 默认 500 行，可用环境变量 MOOX_E2E_KLINE_LIMIT 覆盖（<=0 表示全部）。
func klineRowLimit() int {
	if raw := strings.TrimSpace(os.Getenv("MOOX_E2E_KLINE_LIMIT")); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil {
			return n
		}
	}
	return 500
}

// loadKlines 解析 K 线 CSV 文件。
//
// 文件结构：第 1 行是 GBK 编码的广告横幅（需跳过），第 2 行才是真正的列头：
// candle_begin_time,open,high,low,close,volume,quote_volume,trade_num,...,symbol,...
func loadKlines(path string, limit int) ([]Kline, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开 K 线 CSV 失败: %w", err)
	}
	defer func() { _ = file.Close() }()

	reader := bufio.NewReader(file)
	// 跳过第 1 行广告横幅。
	if _, err := reader.ReadString('\n'); err != nil && err != io.EOF {
		return nil, fmt.Errorf("跳过 CSV 横幅失败: %w", err)
	}

	csvReader := csv.NewReader(reader)
	csvReader.FieldsPerRecord = -1

	header, err := csvReader.Read()
	if err != nil {
		return nil, fmt.Errorf("读取 CSV 列头失败: %w", err)
	}
	idx := headerIndex(header)
	for _, name := range []string{"candle_begin_time", "open", "high", "low", "close", "volume"} {
		if _, ok := idx[name]; !ok {
			return nil, fmt.Errorf("CSV 缺少必需列 %q，实际列头: %v", name, header)
		}
	}

	var out []Kline
	for {
		record, err := csvReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读取 CSV 数据行失败: %w", err)
		}
		k, ok := parseKlineRecord(record, idx)
		if !ok {
			continue
		}
		out = append(out, k)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("CSV 未解析到任何有效 K 线: %s", path)
	}
	return out, nil
}

func headerIndex(header []string) map[string]int {
	idx := make(map[string]int, len(header))
	for i, name := range header {
		idx[strings.TrimSpace(name)] = i
	}
	return idx
}

func parseKlineRecord(record []string, idx map[string]int) (Kline, bool) {
	get := func(name string) string {
		i, ok := idx[name]
		if !ok || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}
	t, err := time.ParseInLocation(csvTimeLayout, get("candle_begin_time"), time.UTC)
	if err != nil {
		return Kline{}, false
	}
	symbol := get("symbol")
	if symbol == "" {
		symbol = "AR-USDT"
	}
	return Kline{
		Time:        t,
		Open:        parseFloat(get("open")),
		High:        parseFloat(get("high")),
		Low:         parseFloat(get("low")),
		Close:       parseFloat(get("close")),
		Volume:      parseFloat(get("volume")),
		QuoteVolume: parseFloat(get("quote_volume")),
		TradeNum:    parseInt(get("trade_num")),
		Symbol:      symbol,
	}, true
}

func parseFloat(value string) float64 {
	if value == "" {
		return 0
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return f
}

func parseInt(value string) int64 {
	if value == "" {
		return 0
	}
	// trade_num 可能是浮点写法，统一按浮点解析后取整。
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return int64(f)
}

// klineTimeSeriesRow 把一条 K 线转换成时序事实数据写入行。
func klineTimeSeriesRow(spaceID, datasetID, subjectID, freq string, k Kline) *pb.TimeSeriesRow {
	return &pb.TimeSeriesRow{
		Key: &pb.TimeSeriesKey{
			SpaceId:   spaceID,
			DatasetId: datasetID,
			SubjectId: subjectID,
			Freq:      freq,
			DataTime:  k.Time.UTC().Format(time.RFC3339),
		},
		Columns: []*pb.ColumnValue{
			testutil.DoubleValue("open", k.Open),
			testutil.DoubleValue("high", k.High),
			testutil.DoubleValue("low", k.Low),
			testutil.DoubleValue("close", k.Close),
			testutil.DoubleValue("volume", k.Volume),
			testutil.DoubleValue("quote_volume", k.QuoteVolume),
			testutil.IntValue("trade_num", k.TradeNum),
			testutil.StringValue("symbol", k.Symbol),
			// note 是一个稳定的全文检索列，保证 SearchRecordRows 有确定可命中的 token。
			testutil.StringValue("note", "kline "+k.Symbol),
		},
	}
}
