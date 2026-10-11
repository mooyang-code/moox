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

	merged, err := mergeExtraConfig(stored, redacted)
	require.NoError(t, err)
	var kept map[string]string
	require.NoError(t, json.Unmarshal([]byte(merged), &kept))
	require.Equal(t, "p-1", kept["passphrase"])
	require.Equal(t, "t-1", kept["api_token"])

	changed, err := mergeExtraConfig(stored, `{"passphrase":"new","region":"x"}`)
	require.NoError(t, err)
	var next map[string]string
	require.NoError(t, json.Unmarshal([]byte(changed), &next))
	require.Equal(t, "new", next["passphrase"])
}

// 嵌套对象与数组里的敏感项同样脱敏，编辑保存往返后沿用原值。
func TestExtraConfigRedactsNestedSensitiveValues(t *testing.T) {
	stored := `{"credentials":{"password":"p-1","user":"u"},"accounts":[{"token":"t-1","name":"a"},{"name":"b","nested":{"api_secret":"s-1"}}],"limit":10}`
	redacted := redactExtraConfig(stored)
	for _, leaked := range []string{"p-1", "t-1", "s-1"} {
		require.NotContains(t, redacted, leaked)
	}
	require.Contains(t, redacted, `"user":"u"`)
	require.Contains(t, redacted, `"limit":10`)
	merged, err := mergeExtraConfig(stored, redacted)
	require.NoError(t, err)
	require.JSONEq(t, stored, merged)
	edited, err := mergeExtraConfig(stored, `{"credentials":{"password":"new","user":"u"},"accounts":[{"token":"******","name":"a"},{"name":"b","nested":{"api_secret":"******"}}],"limit":10}`)
	require.NoError(t, err)
	require.Contains(t, edited, `"password":"new"`)
	require.Contains(t, edited, `"token":"t-1"`)
	require.Contains(t, edited, `"api_secret":"s-1"`)
}

// 数组删除、插入、重排后无法确定占位对应哪个原元素：必须拒绝，不能把别的元素的凭据存进来。
func TestExtraConfigMergeRejectsAmbiguousArrayPlaceholders(t *testing.T) {
	stored := `{"accounts":[{"name":"a","token":"ta"},{"name":"b","token":"tb"}]}`
	for name, submitted := range map[string]string{
		"删除首项": `{"accounts":[{"name":"b","token":"******"}]}`,
		"重排":   `{"accounts":[{"name":"b","token":"******"},{"name":"a","token":"******"}]}`,
		"插入":   `{"accounts":[{"name":"a","token":"******"},{"name":"c","token":"******"},{"name":"b","token":"******"}]}`,
	} {
		_, err := mergeExtraConfig(stored, submitted)
		require.ErrorIs(t, err, errUnresolvedMask, name)
	}
	// 新增元素并为它提供明文凭据、其余元素不变：允许。
	merged, err := mergeExtraConfig(stored, `{"accounts":[{"name":"a","token":"******"},{"name":"b","token":"******"}]}`)
	require.NoError(t, err)
	require.JSONEq(t, stored, merged)
	added, err := mergeExtraConfig(stored, `{"accounts":[{"name":"a","token":"ta"},{"name":"b","token":"tb"},{"name":"c","token":"tc"}]}`)
	require.NoError(t, err)
	require.Contains(t, added, `"tc"`)
}
