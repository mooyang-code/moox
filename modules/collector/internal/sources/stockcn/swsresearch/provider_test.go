package swsresearch

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
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

// classificationFile 是 2026-11-11 录制的真实文件（gzip 压缩存放）。
func classificationFile(t *testing.T) []byte {
	t.Helper()
	compressed, err := os.ReadFile("testdata/StockClassifyUse_stock.xls.gz")
	require.NoError(t, err)
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	require.NoError(t, err)
	body, err := io.ReadAll(reader)
	require.NoError(t, err)
	return body
}

func serve(t *testing.T, handler http.HandlerFunc) *Provider {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return New(Config{BaseURL: server.URL, HTTPClient: server.Client()})
}

func industry(l1, l2, l3 int64) map[string]marketdata.FieldValue {
	return map[string]marketdata.FieldValue{FieldSWLevel1: marketdata.IntField(l1), FieldSWLevel2: marketdata.IntField(l2), FieldSWLevel3: marketdata.IntField(l3)}
}

// 平安银行：2014-02-21 起 480101，2021-07-30 起 480301。
func TestFetchFieldsUsesLatestIndustryNotAfterTradeDate(t *testing.T) {
	body := classificationFile(t)
	var downloads atomic.Int32
	provider := serve(t, func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		assert.Equal(t, "/swindex/pdf/SwClass2021/StockClassifyUse_stock.xls", r.URL.Path)
		_, _ = w.Write(body)
	})
	values, err := provider.FetchFields(context.Background(), marketdata.FieldRequest{SubjectID: "000001.XSHE", Frequency: "1d", Start: day("2021-07-29"), End: day("2021-07-31")})
	require.NoError(t, err)
	require.Len(t, values, 2)
	assert.Equal(t, industry(480000, 480100, 480101), values[day("2021-07-29")], "调整日前一根用上一条")
	assert.Equal(t, industry(480000, 480300, 480301), values[day("2021-07-30")], "调整日当天用新的一条")

	// 计入日期之前没有值；文件里没有的股票、北交所都留空。
	values, err = provider.FetchFields(context.Background(), marketdata.FieldRequest{SubjectID: "000001.XSHE", Frequency: "1d", Start: day("1991-04-02"), End: day("1991-04-04")})
	require.NoError(t, err)
	assert.Equal(t, map[time.Time]map[string]marketdata.FieldValue{day("1991-04-03"): industry(440000, 440100, 440101)}, values)
	for _, subject := range []string{"689999.XSHG", "920992.XBSE"} {
		values, err = provider.FetchFields(context.Background(), marketdata.FieldRequest{SubjectID: subject, Frequency: "1d", Start: day("2026-11-10"), End: day("2026-11-11")})
		require.NoError(t, err)
		assert.Empty(t, values, subject)
	}
	assert.EqualValues(t, 1, downloads.Load(), "一个实例内文件只下载一次")
}

func TestParseClassificationReadsWholeFile(t *testing.T) {
	table, err := parseClassification(classificationFile(t))
	require.NoError(t, err)
	assert.Len(t, table, 5938)
	rows := 0
	for _, changes := range table {
		rows += len(changes)
	}
	assert.Equal(t, 12934, rows)
}

// 文件被截断时要报错，不能出部分结果。文件末尾有几个扇区不属于工作簿数据，只少了它们时内容仍然完整，
// 这种情况允许成功，但必须是全部 12934 行（HTTP 传输中断由 Content-Length 校验拦下，见下一个用例）。
func TestParseClassificationNeverReturnsPartialTable(t *testing.T) {
	body := classificationFile(t)
	for _, size := range []int{0, 100, 512, 4096, len(body) / 10, len(body) / 2, len(body) * 9 / 10, len(body) - 65536, len(body) - 4096, len(body) - 512, len(body) - 1} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			table, err := parseClassification(body[:size])
			if err != nil {
				assert.Nil(t, table)
				return
			}
			rows := 0
			for _, changes := range table {
				rows += len(changes)
			}
			assert.Equal(t, 12934, rows, "成功时必须是完整内容")
			assert.Greater(t, size, len(body)-8192, "只有少了末尾几个扇区才允许成功")
		})
	}
	_, err := parseClassification([]byte("<html>维护中</html>"))
	require.Error(t, err)
}

func TestFetchFieldsFailsWhenDownloadIsTruncatedOrFails(t *testing.T) {
	body := classificationFile(t)
	req := marketdata.FieldRequest{SubjectID: "000001.XSHE", Frequency: "1d", Start: day("2026-11-10"), End: day("2026-11-11")}
	var downloads atomic.Int32
	for name, handler := range map[string]http.HandlerFunc{
		"连接中途断开": func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Length", strconv.Itoa(len(body)))
			_, _ = w.Write(body[:len(body)/2])
		},
		"内容被截断": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body[:len(body)/2]) },
		"服务端错误": func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
	} {
		t.Run(name, func(t *testing.T) {
			downloads.Store(0)
			provider := serve(t, func(w http.ResponseWriter, r *http.Request) {
				downloads.Add(1)
				handler(w, r)
			})
			for range 2 {
				values, err := provider.FetchFields(context.Background(), req)
				require.Error(t, err)
				assert.Nil(t, values)
			}
			assert.EqualValues(t, 1, downloads.Load(), "失败后这个实例不再重试")
		})
	}
}

// 被撤销的那一项不算文件取不到，下一个标的还能取。
func TestFetchFieldsRetriesAfterCallerCancelled(t *testing.T) {
	body := classificationFile(t)
	provider := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(body) })
	req := marketdata.FieldRequest{SubjectID: "000001.XSHE", Frequency: "1d", Start: day("2026-11-10"), End: day("2026-11-11")}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := provider.FetchFields(cancelled, req)
	require.Error(t, err)
	values, err := provider.FetchFields(context.Background(), req)
	require.NoError(t, err)
	assert.Equal(t, industry(480000, 480300, 480301), values[day("2026-11-10")])
}

// 服务器不发中间证书时，带着内嵌的中间证书仍能按系统根证书校验通过；换一个主机名或别的证书就通不过。
func TestTLSConfigVerifiesChainWithEmbeddedIntermediate(t *testing.T) {
	config := tlsConfig()
	require.NotNil(t, config.VerifyConnection)
	assert.Error(t, config.VerifyConnection(tlsState(t, "www.swsresearch.com", nil)), "没有证书")
	// httptest 的自签证书不在系统根证书下，必须被拒绝。
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	_, err := newHTTPClient().Get(server.URL)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "certificate")
}

func TestFieldSpecs(t *testing.T) {
	specs := New(Config{}).FieldSpecs()
	require.Len(t, specs, 3)
	for index, id := range []string{"sw_l1", "sw_l2", "sw_l3"} {
		assert.Equal(t, id, specs[index].ID)
		assert.Equal(t, marketdata.FieldTypeInt, specs[index].Type)
		assert.Equal(t, "prev", specs[index].Fill)
		assert.Equal(t, []string{"1d"}, specs[index].Frequencies)
	}
	require.NoError(t, New(Config{}).Descriptor().Validate())
}

func tlsState(t *testing.T, serverName string, certs []*x509.Certificate) tls.ConnectionState {
	t.Helper()
	return tls.ConnectionState{ServerName: serverName, PeerCertificates: certs}
}

func TestEmbeddedIntermediateCertificate(t *testing.T) {
	block, _ := pem.Decode(intermediatePEM)
	require.NotNil(t, block)
	cert, err := x509.ParseCertificate(block.Bytes)
	require.NoError(t, err)
	assert.Equal(t, "GeoTrust G2 TLS CN RSA4096 SHA256 2022 CA1", cert.Subject.CommonName)
	assert.Equal(t, "DigiCert Global Root G2", cert.Issuer.CommonName)
	assert.True(t, cert.IsCA)
}
