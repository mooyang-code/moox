package binance

import (
	"testing"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStorageBindingHelpers(t *testing.T) {
	market, subject, err := storageBindingKey(InstTypeSPOT)
	require.NoError(t, err)
	assert.Equal(t, "spot", market)
	assert.Equal(t, "spot", subject)

	_, _, err = storageBindingKey("UNKNOWN")
	assert.Error(t, err)

	binding := &StorageBinding{RecordDatasetID: "rec-1", KlineDatasetID: "kline-1"}
	applyBindingDefaults(binding, "spot")
	assert.Equal(t, "binance", binding.DataSourceID)
	assert.Equal(t, "crypto_pair", binding.SubjectType)
	assert.Equal(t, "spot", binding.SubjectMarket)
	assert.ElementsMatch(t, []string{"rec-1", "kline-1"}, binding.BindDatasetIDs)

	ids := appendMissingDatasetIDs([]string{"a", "a", ""}, "b", "a", "c")
	assert.Equal(t, []string{"a", "b", "c"}, ids)

	assert.Equal(t, []string{"x", "y"}, dedupeStrings([]string{"x", "x", "y"}))
}

func TestEnsureStorageOK(t *testing.T) {
	assert.Error(t, ensureStorageOK("act", nil))
	err := ensureStorageOK("act", &storagepb.RetInfo{Code: storagepb.ErrorCode_INNER_ERR, Msg: "fail"})
	assert.ErrorContains(t, err, "fail")
	assert.NoError(t, ensureStorageOK("act", &storagepb.RetInfo{Code: storagepb.ErrorCode_SUCCESS}))
}

func TestStorageFieldBuilders(t *testing.T) {
	assert.Equal(t, "name", stringField("name", "v").GetColumnName())
	assert.Equal(t, int64(3), intField("n", 3).GetValue().GetIntValue())
	assert.Equal(t, 1.5, doubleField("d", 1.5).GetValue().GetDoubleValue())

	auth := storageAuthInfo(StorageBinding{AuthInfo: StorageAuthInfo{AppID: "app", AppKey: "key"}})
	assert.Equal(t, "app", auth.GetAppId())
}

func TestNormalizeStorageTarget(t *testing.T) {
	assert.Equal(t, "ip://127.0.0.1:20102", normalizeStorageTarget("", "20102"))
	assert.Equal(t, "ip://10.0.0.1:20102", normalizeStorageTarget("10.0.0.1:20102", "20102"))
	assert.Equal(t, "ip://host:20100", normalizeStorageTarget("ip://host:20100", "20100"))
	assert.Equal(t, "http://svc:8080", normalizeStorageTarget("http://svc:8080", "20102"))
}
