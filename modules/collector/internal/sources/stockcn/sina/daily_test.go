package sina

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/require"
)

func TestParseDailyPayloadK2ZeroRow(t *testing.T) {
	rows, err := ParseDailyPayload([]byte(`var KLC_K2_data="` + zeroK2Payload() + `";`))
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.NotEmpty(t, rows[0].Date)
	require.Equal(t, "0", rows[0].Open)
	require.Equal(t, "0", rows[0].High)
	require.Equal(t, "0", rows[0].Low)
	require.Equal(t, "0", rows[0].Close)
	require.Equal(t, "0", rows[0].Volume)
	require.Equal(t, "0", rows[0].Amount)
}

func TestParseDailyPayloadRejectsUnsupportedCodec(t *testing.T) {
	_, err := ParseDailyPayload([]byte(`var data="M1/not-k2";`))
	require.ErrorContains(t, err, "unsupported Sina daily codec")
}

func TestK2ScalingReturnsMalformedValueErrorInsteadOfPanicking(t *testing.T) {
	_, err := scaleInteger(int64(^uint64(0)>>1), 0, 9, true)
	require.ErrorContains(t, err, "overflows int64")
}

func TestDailyClientUsesMarketSpecificK2Endpoint(t *testing.T) {
	getter := &recordingGetter{payload: []byte(`var data="` + zeroK2Payload() + `";`)}
	client := NewDailyClient(getter, "stock_hk")
	client.Now = func() time.Time { return time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC) }
	bars, err := client.FetchKlines(context.Background(), marketdata.KlineRequest{
		MarketID: "stock_hk", InstrumentType: "equity", SubjectID: "HK.00700", ProviderSymbol: "HK.00700", Frequency: "1d",
		StartTime: time.Date(1989, 1, 1, 0, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC), Limit: 1,
	})
	require.NoError(t, err)
	require.Len(t, bars, 1)
	require.Equal(t, "sina", bars[0].ProviderID)
	require.Equal(t, "stock_hk_http", bars[0].SourceID)
	require.Equal(t, "share", bars[0].VolumeUnit)
	require.Equal(t, "hkd", bars[0].AmountUnit)
	require.Equal(t, "/stock/hkstock/00700/klc2_kl.js", getter.path)
	require.Equal(t, "catalog_only", string(client.Descriptor().Status))
}

func TestDailyClientDoesNotClaimUSAmount(t *testing.T) {
	client := NewDailyClient(nil, "stock_us")
	require.False(t, client.KlineSpec().HasAmount)
	require.Empty(t, client.KlineSpec().AmountUnit)
}

func zeroK2Payload() string {
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	bits := make([]bool, 0, 128)
	appendBits := func(value, width int) {
		for index := 0; index < width; index++ {
			bits = append(bits, value&(1<<index) != 0)
		}
	}
	bits = append(bits, false)
	for _, width := range []int{9, 9, 9, 9, 15, 15} {
		appendBits(0, width)
	}
	// A changed row with no sub-updates is the K2 stream terminator.
	bits = append(bits, true, true, false, false, false)
	var builder strings.Builder
	for start := 0; start < len(bits); start += 6 {
		value := 0
		for index := 0; index < 6 && start+index < len(bits); index++ {
			if bits[start+index] {
				value |= 1 << index
			}
		}
		builder.WriteByte(alphabet[value])
	}
	return "K2/" + builder.String()
}
