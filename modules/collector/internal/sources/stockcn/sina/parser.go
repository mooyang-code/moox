package sina

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/model/common"
)

type MinuteRow struct {
	Day    string
	Open   string
	High   string
	Low    string
	Close  string
	Volume string
	Amount string
}

// ParseMinutePayload extracts the JSON array from either JSONP form used by
// Sina. It accepts no JavaScript expressions beyond the surrounding callback.
func ParseMinutePayload(raw []byte) ([]MinuteRow, error) {
	body, err := jsonArray(raw)
	if err != nil {
		return nil, err
	}
	var values []map[string]json.RawMessage
	if err := json.Unmarshal(body, &values); err != nil {
		return nil, fmt.Errorf("decode JSONP array: %w", err)
	}
	result := make([]MinuteRow, 0, len(values))
	for index, value := range values {
		row, err := parseMinuteRow(value)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", index, err)
		}
		result = append(result, row)
	}
	return result, nil
}

func jsonArray(raw []byte) ([]byte, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, fmt.Errorf("empty response")
	}
	start := bytes.IndexByte(raw, '[')
	if start < 0 {
		return nil, fmt.Errorf("JSONP payload has no JSON array")
	}
	end := bytes.LastIndexByte(raw, ']')
	if end < start {
		return nil, fmt.Errorf("JSONP payload has an incomplete JSON array")
	}
	return raw[start : end+1], nil
}

func parseMinuteRow(value map[string]json.RawMessage) (MinuteRow, error) {
	row := MinuteRow{}
	var err error
	if row.Day, err = requiredString(value, "day", "date"); err != nil {
		return MinuteRow{}, err
	}
	if row.Open, err = requiredNumber(value, "open"); err != nil {
		return MinuteRow{}, err
	}
	if row.High, err = requiredNumber(value, "high"); err != nil {
		return MinuteRow{}, err
	}
	if row.Low, err = requiredNumber(value, "low"); err != nil {
		return MinuteRow{}, err
	}
	if row.Close, err = requiredNumber(value, "close"); err != nil {
		return MinuteRow{}, err
	}
	if row.Volume, err = requiredNumber(value, "volume"); err != nil {
		return MinuteRow{}, err
	}
	if row.Amount, err = requiredNumber(value, "amount"); err != nil {
		return MinuteRow{}, err
	}
	return row, nil
}

func requiredString(value map[string]json.RawMessage, names ...string) (string, error) {
	for _, name := range names {
		data, ok := value[name]
		if !ok {
			continue
		}
		data = bytes.TrimSpace(data)
		if len(data) == 0 || bytes.Equal(data, []byte("null")) {
			return "", fmt.Errorf("%s is empty", name)
		}
		var result string
		if data[0] == '"' {
			if err := json.Unmarshal(data, &result); err != nil {
				return "", fmt.Errorf("%s: %w", name, err)
			}
		} else {
			result = string(data)
		}
		if strings.TrimSpace(result) == "" {
			return "", fmt.Errorf("%s is empty", name)
		}
		return strings.TrimSpace(result), nil
	}
	return "", fmt.Errorf("missing %s", strings.Join(names, " or "))
}

func requiredNumber(value map[string]json.RawMessage, name string) (string, error) {
	result, err := requiredString(value, name)
	if err != nil {
		return "", err
	}
	number, err := strconv.ParseFloat(result, 64)
	if err != nil {
		return "", fmt.Errorf("%s %q is not numeric", name, result)
	}
	if math.IsNaN(number) || math.IsInf(number, 0) {
		return "", fmt.Errorf("%s %q is not finite", name, result)
	}
	return result, nil
}

func parseTimestamp(value string, location *time.Location) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04:05", "2006-01-02 15:04", "20060102 15:04:05"} {
		if parsed, err := time.ParseInLocation(layout, strings.TrimSpace(value), location); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}

func newDecimal(value string) common.Decimal { return common.NewDecimal(value) }
