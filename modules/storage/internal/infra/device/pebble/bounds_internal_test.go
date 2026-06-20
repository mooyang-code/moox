package pebble

import (
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/infra/device/factkey"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
)

func TestEncodeRowKeyUsesTimeSeriesPrefixAndOrdersVersionAfterFreq(t *testing.T) {
	scope := &pb.DataScope{
		SpaceId:    "crypto",
		DatasetId:  "kline",
		SubjectId:  "APT-USDT",
		Freq:       "1m",
		Dimensions: map[string]string{"market": "spot"},
	}

	key := encodeRowKey(&pb.DataRow{Key: &pb.DataKey{Scope: scope, DataTime: "2026-06-15T00:00:00Z", RowId: "row-1"}})

	require.Equal(t, strings.Join([]string{
		timeSeriesKeyPrefix,
		"crypto",
		"kline",
		"APT-USDT",
		"1m",
		"2026-06-15T00:00:00.000000000Z",
		factkey.DimensionsHash(scope.GetDimensions()),
		"row-1",
	}, "|"), key)
}

func TestEncodeRowKeyUsesObjectPrefixForNonTimeSeriesRows(t *testing.T) {
	scope := &pb.DataScope{
		SpaceId:   "crypto",
		DatasetId: "symbols",
		SubjectId: "APT-USDT",
	}

	key := encodeRowKey(&pb.DataRow{Key: &pb.DataKey{Scope: scope, RowId: "ARB-USDT"}})

	require.Equal(t, strings.Join([]string{
		objectKeyPrefix,
		"crypto",
		"symbols",
		"ARB-USDT",
		factkey.EmptyVersion,
	}, "|"), key)
}

func TestReadBoundsUsesFreqPrefixWhenNoTimeRangeEvenWithDimensions(t *testing.T) {
	scope := &pb.DataScope{
		SpaceId:    "crypto",
		DatasetId:  "kline",
		SubjectId:  "APT-USDT",
		Freq:       "1m",
		Dimensions: map[string]string{"market": "spot"},
	}

	lower, upper := readBounds(scope, nil)

	prefix := strings.Join([]string{
		timeSeriesKeyPrefix,
		"crypto",
		"kline",
		"APT-USDT",
		"1m",
	}, "|") + "|"
	require.Equal(t, prefix, string(lower))
	require.Equal(t, string(nextPrefix([]byte(prefix))), string(upper))
}

func TestReadBoundsNormalizesRFC3339TimesToUTC(t *testing.T) {
	scope := &pb.DataScope{
		SpaceId:    "crypto",
		DatasetId:  "kline",
		SubjectId:  "APT-USDT",
		Freq:       "1m",
		Dimensions: map[string]string{"market": "spot"},
	}

	lower, upper := readBounds(scope, &pb.TimeRange{
		StartTime: "2026-06-15T00:00:00+08:00",
		EndTime:   "2026-06-15T00:01:00+08:00",
	})

	prefix := strings.Join([]string{
		timeSeriesKeyPrefix,
		"crypto",
		"kline",
		"APT-USDT",
		"1m",
	}, "|") + "|"
	require.Equal(t, prefix+"2026-06-14T16:00:00.000000000Z", string(lower))
	require.Equal(t, string(nextPrefix([]byte(prefix+"2026-06-14T16:01:00.000000000Z"))), string(upper))
}

func TestReadBoundsUsesTimeRangeAfterFreqWithoutDimensions(t *testing.T) {
	scope := &pb.DataScope{
		SpaceId:   "crypto",
		DatasetId: "kline",
		SubjectId: "APT-USDT",
		Freq:      "1m",
	}

	lower, upper := readBounds(scope, &pb.TimeRange{
		StartTime: "2026-06-15T00:00:00Z",
		EndTime:   "2026-06-15T00:00:00Z",
	})

	prefix := strings.Join([]string{
		timeSeriesKeyPrefix,
		"crypto",
		"kline",
		"APT-USDT",
		"1m",
	}, "|") + "|"
	require.Equal(t, prefix+"2026-06-15T00:00:00.000000000Z", string(lower))
	require.Equal(t, string(nextPrefix([]byte(prefix+"2026-06-15T00:00:00.000000000Z"))), string(upper))
}

func TestNormalizeKeyTimeUsesLexicographicTimeOrder(t *testing.T) {
	wholeSecond, ok := normalizeKeyTime("2026-01-01T00:00:00Z")
	require.True(t, ok)
	halfSecond, ok := normalizeKeyTime("2026-01-01T00:00:00.5Z")
	require.True(t, ok)

	require.Less(t, wholeSecond, halfSecond)
	require.Equal(t, "2026-01-01T00:00:00.000000000Z", wholeSecond)
	require.Equal(t, "2026-01-01T00:00:00.500000000Z", halfSecond)
}

func TestReadBoundsKeepsDimensionWildcardWhenDimensionsAreNotSpecified(t *testing.T) {
	scope := &pb.DataScope{
		SpaceId:   "crypto",
		DatasetId: "kline",
		SubjectId: "APT-USDT",
		Freq:      "1m",
	}

	lower, upper := readBounds(scope, nil)

	prefix := strings.Join([]string{timeSeriesKeyPrefix, "crypto", "kline", "APT-USDT", "1m"}, "|") + "|"
	require.Equal(t, prefix, string(lower))
	require.Equal(t, string(nextPrefix([]byte(prefix))), string(upper))
}
