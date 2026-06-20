package bench

import (
	"bufio"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const klineTimeLayout = "2006-01-02 15:04:05"

type KlineFile struct {
	Path      string `json:"path"`
	Market    string `json:"market"`
	SubjectID string `json:"subject_id"`
	Freq      string `json:"freq"`
}

type KlineRow struct {
	Time                     time.Time
	Open                     float64
	High                     float64
	Low                      float64
	Close                    float64
	Volume                   float64
	QuoteVolume              float64
	TradeNum                 int64
	TakerBuyBaseAssetVolume  float64
	TakerBuyQuoteAssetVolume float64
	Symbol                   string
	AvgPrice1M               float64
	AvgPrice5M               float64
	FundingRate              *float64
}

func DiscoverKlineFiles(root string, freq string) ([]KlineFile, error) {
	base := filepath.Join(root, "period_"+freq+"_kline")
	var files []KlineFile
	for _, market := range []string{"spot", "swap"} {
		dir := filepath.Join(base, market)
		entries, err := os.ReadDir(dir)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, err
		}
		for _, entry := range entries {
			if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".csv" {
				continue
			}
			subjectID := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
			files = append(files, KlineFile{
				Path:      filepath.Join(dir, entry.Name()),
				Market:    market,
				SubjectID: subjectID,
				Freq:      freq,
			})
		}
	}
	sort.Slice(files, func(i, j int) bool {
		if files[i].Market != files[j].Market {
			return files[i].Market < files[j].Market
		}
		return files[i].SubjectID < files[j].SubjectID
	})
	if len(files) == 0 {
		return nil, fmt.Errorf("no %s kline CSV files found under %s", freq, root)
	}
	return files, nil
}

func ReadKlineCSV(file KlineFile, limit int) ([]KlineRow, error) {
	f, err := os.Open(file.Path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	reader := bufio.NewReader(f)
	csvReader := csv.NewReader(reader)
	csvReader.FieldsPerRecord = -1
	csvReader.LazyQuotes = true
	header, err := readKlineHeader(csvReader)
	if err != nil {
		return nil, err
	}
	index := headerIndex(header)
	for _, name := range []string{"candle_begin_time", "open", "high", "low", "close", "volume", "quote_volume", "trade_num", "symbol"} {
		if _, ok := index[name]; !ok {
			return nil, fmt.Errorf("%s missing required column %s", file.Path, name)
		}
	}

	var rows []KlineRow
	line := 1
	for {
		record, err := csvReader.Read()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, fmt.Errorf("%s row %d: %w", file.Path, line+1, err)
		}
		line++
		if emptyRecord(record) {
			continue
		}
		row, err := parseKlineRecord(record, index)
		if err != nil {
			return nil, fmt.Errorf("%s row %d: %w", file.Path, line, err)
		}
		rows = append(rows, row)
		if limit > 0 && len(rows) >= limit {
			break
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("%s has no kline rows", file.Path)
	}
	return rows, nil
}

func readKlineHeader(reader *csv.Reader) ([]string, error) {
	for {
		record, err := reader.Read()
		if err != nil {
			return nil, err
		}
		for _, value := range record {
			if strings.TrimSpace(value) == "candle_begin_time" {
				return record, nil
			}
		}
	}
}

func headerIndex(header []string) map[string]int {
	out := make(map[string]int, len(header))
	for i, name := range header {
		out[strings.TrimSpace(name)] = i
	}
	return out
}

func parseKlineRecord(record []string, index map[string]int) (KlineRow, error) {
	get := func(name string) string {
		i, ok := index[name]
		if !ok || i >= len(record) {
			return ""
		}
		return strings.TrimSpace(record[i])
	}
	parsedTime, err := time.ParseInLocation(klineTimeLayout, get("candle_begin_time"), time.UTC)
	if err != nil {
		return KlineRow{}, err
	}
	fundingRate, err := optionalFloat(get("fundingRate"))
	if err != nil {
		return KlineRow{}, fmt.Errorf("fundingRate: %w", err)
	}
	return KlineRow{
		Time:                     parsedTime,
		Open:                     mustFloat(get("open")),
		High:                     mustFloat(get("high")),
		Low:                      mustFloat(get("low")),
		Close:                    mustFloat(get("close")),
		Volume:                   mustFloat(get("volume")),
		QuoteVolume:              mustFloat(get("quote_volume")),
		TradeNum:                 mustInt(get("trade_num")),
		TakerBuyBaseAssetVolume:  mustFloat(get("taker_buy_base_asset_volume")),
		TakerBuyQuoteAssetVolume: mustFloat(get("taker_buy_quote_asset_volume")),
		Symbol:                   get("symbol"),
		AvgPrice1M:               mustFloat(get("avg_price_1m")),
		AvgPrice5M:               mustFloat(get("avg_price_5m")),
		FundingRate:              fundingRate,
	}, nil
}

func emptyRecord(record []string) bool {
	for _, value := range record {
		if strings.TrimSpace(value) != "" {
			return false
		}
	}
	return true
}

func mustFloat(value string) float64 {
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return parsed
}

func optionalFloat(value string) (*float64, error) {
	if value == "" {
		return nil, nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func mustInt(value string) int64 {
	if value == "" {
		return 0
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return int64(parsed)
}
