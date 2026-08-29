package stockcn

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

var shanghaiLocation = mustLoadLocation("Asia/Shanghai")

func CanonicalSubjectID(code string) (string, error) {
	code = strings.TrimSpace(code)
	if len(code) != 6 {
		return "", fmt.Errorf("%w: China security code must contain six digits", marketdata.ErrUnsupportedSymbol)
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return "", fmt.Errorf("%w: invalid security code", marketdata.ErrUnsupportedSymbol)
		}
	}
	switch code[0] {
	case '6':
		return code + ".XSHG", nil
	case '0', '2', '3':
		return code + ".XSHE", nil
	case '4', '8', '9':
		return code + ".XBSE", nil
	default:
		return "", fmt.Errorf("%w: unsupported exchange for code %s", marketdata.ErrUnsupportedSymbol, code)
	}
}

func ProviderSymbol(subjectID string) (string, error) {
	code, exchange, err := splitSubjectID(subjectID)
	if err != nil {
		return "", err
	}
	switch exchange {
	case "XSHG":
		return "sh" + code, nil
	case "XSHE":
		return "sz" + code, nil
	case "XBSE":
		return "bj" + code, nil
	default:
		return "", fmt.Errorf("%w: unknown exchange %s", marketdata.ErrUnsupportedSymbol, exchange)
	}
}

func DecodeProviderSymbol(symbol string) (string, error) {
	symbol = strings.ToLower(strings.TrimSpace(symbol))
	if len(symbol) != 8 {
		return "", fmt.Errorf("%w: invalid provider symbol %q", marketdata.ErrUnsupportedSymbol, symbol)
	}
	code := symbol[2:]
	switch symbol[:2] {
	case "sh":
		return CanonicalSubjectIDWithExchange(code, "XSHG")
	case "sz":
		return CanonicalSubjectIDWithExchange(code, "XSHE")
	case "bj":
		return CanonicalSubjectIDWithExchange(code, "XBSE")
	default:
		return "", fmt.Errorf("%w: invalid provider symbol prefix %q", marketdata.ErrUnsupportedSymbol, symbol[:2])
	}
}

func CanonicalSubjectIDWithExchange(code, exchange string) (string, error) {
	subjectID, err := CanonicalSubjectID(code)
	if err != nil {
		return "", err
	}
	if !strings.HasSuffix(subjectID, "."+exchange) {
		return "", fmt.Errorf("%w: code %s does not belong to exchange %s", marketdata.ErrUnsupportedSymbol, code, exchange)
	}
	return subjectID, nil
}

func EastMoneySecID(subjectID string) (string, error) {
	code, exchange, err := splitSubjectID(subjectID)
	if err != nil {
		return "", err
	}
	switch exchange {
	case "XSHG":
		return "1." + code, nil
	case "XSHE", "XBSE":
		return "0." + code, nil
	default:
		return "", fmt.Errorf("%w: unknown exchange %s", marketdata.ErrUnsupportedSymbol, exchange)
	}
}

func ParseCNMinute(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	layouts := []string{"2006-01-02 15:04", "2006-01-02 15:04:05", "200601021504"}
	for _, layout := range layouts {
		if ts, err := time.ParseInLocation(layout, value, shanghaiLocation); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("%w: invalid minute timestamp %q", marketdata.ErrProtocol, value)
}

func ParseFloat(value string) (float64, error) {
	number, err := strconv.ParseFloat(strings.TrimSpace(strings.ReplaceAll(value, ",", "")), 64)
	if err != nil {
		return 0, fmt.Errorf("%w: parse float %q: %v", marketdata.ErrProtocol, value, err)
	}
	return number, nil
}

func NormalizeMinuteKline(subjectID, providerID, providerSymbol string, mode marketdata.TimestampMode, rawTime string, open, high, low, close, volume, amount float64, volumeMultiplier float64, fetchedAt time.Time, requestID string) (marketdata.NormalizedKline, error) {
	barTime, err := ParseCNMinute(rawTime)
	if err != nil {
		return marketdata.NormalizedKline{}, err
	}
	barStart := barTime
	barEnd := barTime.Add(time.Minute)
	if mode == marketdata.TimestampModeClose {
		barEnd = barTime
		barStart = barEnd.Add(-time.Minute)
	}
	row := marketdata.NormalizedKline{
		SubjectID:         subjectID,
		ProviderID:        providerID,
		ProviderSymbol:    providerSymbol,
		Frequency:         string(marketdata.FrequencyMinute),
		BarStart:          barStart.UTC(),
		BarEnd:            barEnd.UTC(),
		Open:              open,
		High:              high,
		Low:               low,
		Close:             close,
		VolumeShares:      volume * volumeMultiplier,
		AmountCNY:         amount,
		ProviderTimestamp: barEnd.UTC(),
		FetchedAt:         fetchedAt.UTC(),
		RequestID:         requestID,
	}
	if err := marketdata.ValidateNormalizedKline(row); err != nil {
		return marketdata.NormalizedKline{}, err
	}
	return row, nil
}

func splitSubjectID(subjectID string) (string, string, error) {
	parts := strings.Split(strings.TrimSpace(subjectID), ".")
	if len(parts) != 2 {
		return "", "", fmt.Errorf("%w: invalid subject_id %q", marketdata.ErrUnsupportedSymbol, subjectID)
	}
	if len(parts[0]) != 6 {
		return "", "", fmt.Errorf("%w: invalid subject code %q", marketdata.ErrUnsupportedSymbol, parts[0])
	}
	return parts[0], parts[1], nil
}

func mustLoadLocation(name string) *time.Location {
	location, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return location
}
