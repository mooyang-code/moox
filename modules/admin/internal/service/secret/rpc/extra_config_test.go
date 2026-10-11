package rpc

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

// 只读接口不回显 OKX passphrase 等敏感项；保存时回传占位表示保持原值。
func TestExtraConfigRedactionAndMerge(t *testing.T) {
	stored := `{"passphrase":"p-1","region":"ap-guangzhou","api_token":"t-1"}`
	redacted := redactExtraConfig(stored)
	var object map[string]string
	require.NoError(t, json.Unmarshal([]byte(redacted), &object))
	require.Equal(t, map[string]string{"passphrase": "******", "region": "ap-guangzhou", "api_token": "******"}, object)
	require.NotContains(t, redacted, "p-1")
	require.Equal(t, maskedExtraValue, redactExtraConfig(`not json`), "无法解析时整体隐藏")
	require.Equal(t, "", redactExtraConfig(""))

	merged := mergeExtraConfig(stored, redacted)
	var kept map[string]string
	require.NoError(t, json.Unmarshal([]byte(merged), &kept))
	require.Equal(t, "p-1", kept["passphrase"])
	require.Equal(t, "t-1", kept["api_token"])

	changed := mergeExtraConfig(stored, `{"passphrase":"new","region":"x"}`)
	var next map[string]string
	require.NoError(t, json.Unmarshal([]byte(changed), &next))
	require.Equal(t, "new", next["passphrase"])
}
