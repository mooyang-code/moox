package command

import (
	"testing"

	"github.com/spf13/cobra"

	"github.com/stretchr/testify/require"
)

// 未显式传 --output-field 时默认不带 output_fields，由服务端按市场或源数据集输出全部默认字段；不再写死 crypto 专属 7 列。
func TestCollectorTaskCreateParamsOutputFields(t *testing.T) {
	defaultFlag := collectorTaskCreateCmd.Flags().Lookup("output-field")
	require.NotNil(t, defaultFlag)
	require.Equal(t, "[]", defaultFlag.DefValue)

	empty := collectorTaskCreateParams(collectorTaskCreateFlags{TagIDs: []string{"hk_hsi"}, Frequency: "5m"})
	require.NotContains(t, empty, "output_fields")
	require.Equal(t, "5m", empty["frequency"])

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

// 合成任务只发源任务与目标周期；没显式给 --output-field 时不带 output_fields，输出合成的全部固定字段（资金费率、股票附加字段不合成）。
func TestCollectorResampleTaskParams(t *testing.T) {
	flags := collectorTaskCreateFlags{SourceTaskID: " src1 ", Frequency: "5m", Fields: []string{"open", "close"}}
	require.Equal(t, map[string]any{"source_task_id": "src1", "target_frequency": "5m"}, collectorResampleTaskParams(flags, false))
	params := collectorResampleTaskParams(flags, true)
	require.Equal(t, []any{"open", "close"}, params["output_fields"])
}

// 合成任务必须显式给目标周期，不能带标的、采集方式等由源任务决定的参数。
func TestValidateResampleCreateFlags(t *testing.T) {
	newCmd := func(args ...string) *cobra.Command {
		cmd := &cobra.Command{Use: "create"}
		var v collectorTaskCreateFlags
		cmd.Flags().StringVar(&v.Frequency, "frequency", "1m", "")
		cmd.Flags().StringSliceVar(&v.TagIDs, "tag-id", nil, "")
		cmd.Flags().StringVar(&v.Ingest, "ingest", "", "")
		cmd.Flags().IntVar(&v.Symbols, "symbols-per-function", 0, "")
		cmd.Flags().IntVar(&v.Lateness, "allowed-lateness-seconds", 0, "")
		cmd.Flags().BoolVar(&v.AllFields, "all-output-fields", false, "")
		require.NoError(t, cmd.Flags().Parse(args))
		return cmd
	}
	require.ErrorContains(t, validateResampleCreateFlags(newCmd()), "--frequency")
	require.NoError(t, validateResampleCreateFlags(newCmd("--frequency", "5m")))
	require.ErrorContains(t, validateResampleCreateFlags(newCmd("--frequency", "5m", "--tag-id", "x")), "--tag-id")
	require.ErrorContains(t, validateResampleCreateFlags(newCmd("--frequency", "5m", "--ingest", "scf")), "--ingest")
}
