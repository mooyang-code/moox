package command

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// --all-output-fields 不带 output_fields：Collector 把空的输出字段当作该品种行的全部字段，与种子里不写 output_fields 的任务
// （A 股 5m、港股 5m）建出的结果数据集相同。不带这个开关时仍发 --output-field 的值（默认是现货、永续行的字段）。
func TestCollectorTaskCreateParamsOutputFields(t *testing.T) {
	flags := collectorTaskCreateFlags{TagIDs: []string{"hk_hsi"}, Frequency: "5m", Fields: []string{"open", "close"}}
	params := collectorTaskCreateParams(flags)
	require.Equal(t, []any{"open", "close"}, params["output_fields"])
	require.Equal(t, "5m", params["frequency"])
	require.Equal(t, []any{"hk_hsi"}, params["subject_tags"])

	flags.AllFields = true
	params = collectorTaskCreateParams(flags)
	require.NotContains(t, params, "output_fields")
	require.Equal(t, "5m", params["frequency"])

	require.NotNil(t, collectorTaskCreateCmd.Flags().Lookup("all-output-fields"))
}

// 两个字段参数都没给时提示用的是默认字段清单；给了任意一个就不提示。不看空间和市场。
func TestCollectorTaskCreateFieldsHint(t *testing.T) {
	flags := collectorTaskCreateFlags{Fields: []string{"close", "volume"}}
	hint := collectorTaskCreateFieldsHint(flags, false)
	require.Contains(t, hint, "默认字段清单（close,volume）")
	require.Contains(t, hint, "--all-output-fields")
	require.Empty(t, collectorTaskCreateFieldsHint(flags, true))
	flags.AllFields = true
	require.Empty(t, collectorTaskCreateFieldsHint(flags, false))
}
