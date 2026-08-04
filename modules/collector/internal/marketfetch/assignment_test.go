package marketfetch

import (
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/stretchr/testify/require"
)

func TestBuildAssignmentsStableAndBounded(t *testing.T) {
	subjects := make([]string, 0, 61)
	for i := 0; i < 61; i++ {
		subjects = append(subjects, fmt.Sprintf("S%02d-USDT", i))
	}
	nodes := []scfinvoker.Node{{NodeID: "n2", Region: "ap-shanghai", NodeType: "scf-event", TriggerType: "timer"}, {NodeID: "n1", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}, {NodeID: "n3", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}}
	assignments, err := BuildAssignments([]TaskGroup{{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: subjects}}, nodes, 30)
	require.NoError(t, err)
	require.Len(t, assignments, 3)
	for _, assignment := range assignments {
		require.LessOrEqual(t, len(assignment.Subjects), 30)
		require.True(t, assignment.Enabled)
	}
	assignments2, err := BuildAssignments([]TaskGroup{{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: append([]string(nil), subjects...)}}, nodes, 30)
	require.NoError(t, err)
	require.Equal(t, assignments, assignments2)
}

func TestBuildAssignmentsRejectsCapacity(t *testing.T) {
	_, err := BuildAssignments([]TaskGroup{{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: []string{"BTC-USDT", "ETH-USDT"}}}, []scfinvoker.Node{{NodeID: "n", NodeType: "scf-event", TriggerType: "timer"}}, 1)
	require.ErrorContains(t, err, "capacity")
}

func TestCronForFrequency(t *testing.T) {
	cron, err := CronForFrequency("1m")
	require.NoError(t, err)
	require.Equal(t, "0 * * * * * *", cron)
	_, err = CronForFrequency("2m")
	require.Error(t, err)
}
