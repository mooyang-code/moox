package sinahfq

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func day(value string) time.Time {
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		panic(err)
	}
	return parsed
}

func serve(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(Config{BaseURL: server.URL})
}

// 录制的贵州茅台真实响应：2026-06-26 除权，前一条是 2025-12-19。
func TestFetchFieldsUsesLatestFactorNotAfterTradeDate(t *testing.T) {
	body, err := os.ReadFile("testdata/hfq_sh600519.js")
	require.NoError(t, err)
	provider := serve(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/realstock/company/sh600519/hfq.js", r.URL.Path)
		_, _ = w.Write(body)
	})
	values, err := provider.FetchFields(context.Background(), marketdata.FieldRequest{SubjectID: "600519.XSHG", Frequency: "1d", Start: day("2026-06-25"), End: day("2026-06-27")})
	require.NoError(t, err)
	require.Len(t, values, 2)
	assert.Equal(t, marketdata.DoubleField(8.6771454751862), values[day("2026-06-25")][FieldAdjFactor], "除权日前一根用上一条")
	assert.Equal(t, marketdata.DoubleField(8.8825126710204), values[day("2026-06-26")][FieldAdjFactor], "除权日当天用新的一条")

	// 上市前的日期落在 1900-01-01 那一条上，因子是 1。
	values, err = provider.FetchFields(context.Background(), marketdata.FieldRequest{SubjectID: "600519.XSHG", Frequency: "1d", Start: day("2001-08-01"), End: day("2001-08-02")})
	require.NoError(t, err)
	assert.Equal(t, marketdata.DoubleField(1), values[day("2001-08-01")][FieldAdjFactor])
}

func TestFetchFieldsHasNoValueForUnknownOrUnsupportedStock(t *testing.T) {
	requests := 0
	provider := serve(t, func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.NotFound(w, r)
	})
	req := marketdata.FieldRequest{SubjectID: "301999.XSHE", Frequency: "1d", Start: day("2026-06-25"), End: day("2026-06-26")}
	values, err := provider.FetchFields(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, values, "来源没有这只股票")

	req.SubjectID = "920992.XBSE"
	values, err = provider.FetchFields(context.Background(), req)
	require.NoError(t, err)
	assert.Empty(t, values, "北交所不支持")
	assert.Equal(t, 1, requests, "不支持的标的不发请求")
}

func TestFetchFieldsFailsOnBadResponse(t *testing.T) {
	req := marketdata.FieldRequest{SubjectID: "600519.XSHG", Frequency: "1d", Start: day("2026-06-25"), End: day("2026-06-26")}
	for name, handler := range map[string]http.HandlerFunc{
		"服务端错误": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) },
		"响应被截断": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`var sh600519hfq={"total":33,"data":[{"d":"2026-06-26", "f":"8.88"},{"d":"2025-`))
		},
		"因子不是数字": func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`var sh600519hfq={"total":1,"data":[{"d":"2026-06-26", "f":"abc"}]}`))
		},
	} {
		t.Run(name, func(t *testing.T) {
			values, err := serve(t, handler).FetchFields(context.Background(), req)
			require.Error(t, err)
			assert.Nil(t, values)
		})
	}
}

func TestFieldSpecs(t *testing.T) {
	specs := New(Config{}).FieldSpecs()
	require.Len(t, specs, 1)
	assert.Equal(t, marketdata.FieldSpec{ID: "adj_factor", Name: "后复权因子", Type: marketdata.FieldTypeDouble, Fill: "prev", Frequencies: []string{"1d"}}, specs[0])
	require.NoError(t, New(Config{}).Descriptor().Validate())
}
