package marketfetch

import (
	"fmt"
	"sort"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/stretchr/testify/require"
)

func TestBuildStockCNAssignmentsUsesEveryTimerNodeAndKeepsExistingSubjectsStable(t *testing.T) {
	subjects := []string{"600000.XSHG", "000001.XSHE", "601318.XSHG", "300750.XSHE", "000858.XSHE"}
	externals := stockCNExternalSymbols(subjects)
	nodes := []scfinvoker.Node{
		{NodeID: "node-c", FunctionName: "moox-stock-cn-002", Region: "ap-shanghai", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "node-a", FunctionName: "moox-stock-cn-000", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "node-b", FunctionName: "moox-stock-cn-001", Region: "ap-beijing", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "worker", FunctionName: "not-a-timer", NodeType: "scf-resident", TriggerType: "http"},
	}
	group := TaskGroup{Provider: "stock_cn_multi", MarketType: "equity", DatasetID: StockCNDatasetID, Frequency: "1m", Subjects: subjects, ExternalSymbols: externals}

	assignments, err := BuildStockCNAssignments(group, nodes, stockCNMaxSubjects, "2026-08-29")
	require.NoError(t, err)
	require.Len(t, assignments, 3)
	seen := make(map[string]int, len(subjects))
	before := make(map[string]string, len(subjects))
	for index, assignment := range assignments {
		require.True(t, assignment.Enabled)
		require.Equal(t, index, assignment.GroupID)
		require.Equal(t, StockCNRouteID, assignment.RouteVersion)
		require.Len(t, assignment.ProviderChain, 3)
		require.Equal(t, assignment.ProviderChain[0], assignment.Provider)
		for _, subject := range assignment.Subjects {
			seen[subject]++
			before[subject] = assignment.NodeID
		}
	}
	for _, subject := range subjects {
		require.Equal(t, 1, seen[subject])
	}

	group.Subjects = append(group.Subjects, "688981.XSHG")
	group.ExternalSymbols["688981.XSHG"] = "sh688981"
	after, err := BuildStockCNAssignments(group, append([]scfinvoker.Node(nil), nodes...), stockCNMaxSubjects, "2026-08-29")
	require.NoError(t, err)
	for _, assignment := range after {
		for _, subject := range assignment.Subjects {
			if nodeID, exists := before[subject]; exists {
				require.Equal(t, nodeID, assignment.NodeID, "adding one subject must not move %s", subject)
			}
		}
	}
}

func TestBuildStockCNAssignmentsFitsApproximateFullMarketIntoTwoHundredGroups(t *testing.T) {
	subjects := make([]string, 0, 5550)
	for index := 0; index < 5550; index++ {
		subjects = append(subjects, fmt.Sprintf("%06d.XSHG", index))
	}
	nodes := make([]scfinvoker.Node, 0, 200)
	for index := 0; index < 200; index++ {
		nodes = append(nodes, scfinvoker.Node{NodeID: fmt.Sprintf("node-%03d", index), FunctionName: fmt.Sprintf("moox-stock-cn-%03d", index), NodeType: "scf-event", TriggerType: "timer"})
	}
	group := TaskGroup{Provider: "stock_cn_multi", MarketType: "equity", DatasetID: StockCNDatasetID, Frequency: "1m", Subjects: subjects, ExternalSymbols: stockCNExternalSymbols(subjects)}

	assignments, err := BuildStockCNAssignments(group, nodes, stockCNMaxSubjects, "2026-08-29")
	require.NoError(t, err)
	require.Len(t, assignments, 200)
	assigned := 0
	before := make(map[string]int, len(subjects))
	for _, assignment := range assignments {
		require.LessOrEqual(t, len(assignment.Subjects), stockCNMaxSubjects)
		_, err := buildManagedEnvironment(assignment, nil, stockCNMaxManagedEnvironmentSize)
		require.NoError(t, err)
		for _, subject := range assignment.Subjects {
			before[subject] = assignment.GroupID
		}
		assigned += len(assignment.Subjects)
	}
	require.Equal(t, len(subjects), assigned)

	group.Subjects = append(group.Subjects, "999999.XSHE")
	group.ExternalSymbols["999999.XSHE"] = "sz999999"
	after, err := BuildStockCNAssignments(group, nodes, stockCNMaxSubjects, "2026-08-29")
	require.NoError(t, err)
	moved := 0
	for _, assignment := range after {
		for _, subject := range assignment.Subjects {
			if previous, exists := before[subject]; exists && previous != assignment.GroupID {
				moved++
			}
		}
	}
	require.LessOrEqual(t, moved, 1, "one-symbol universe growth should move at most one existing subject")
}

func TestBuildStockCNAssignmentsKeepsPublishedFleetWhenUniverseIsSmall(t *testing.T) {
	subjects := []string{"600000.XSHG", "000001.XSHE"}
	nodes := make([]scfinvoker.Node, 0, 5)
	for index := 0; index < 5; index++ {
		nodes = append(nodes, scfinvoker.Node{NodeID: fmt.Sprintf("node-%d", index), FunctionName: fmt.Sprintf("moox-stock-cn-%03d", index), NodeType: "scf-event", TriggerType: "timer"})
	}
	assignments, err := BuildStockCNAssignments(TaskGroup{
		Provider: "stock_cn_multi", MarketType: "equity", DatasetID: StockCNDatasetID, Frequency: "1m",
		Subjects: subjects, ExternalSymbols: stockCNExternalSymbols(subjects),
	}, nodes, stockCNMaxSubjects, "2026-08-29")
	require.NoError(t, err)
	require.Len(t, assignments, len(nodes))
	assigned := 0
	for groupID, assignment := range assignments {
		require.Equal(t, groupID, assignment.GroupID)
		require.Equal(t, len(assignment.Subjects) > 0, assignment.Enabled)
		assigned += len(assignment.Subjects)
	}
	require.Equal(t, len(subjects), assigned)
}

func TestBuildAssignmentsKeepsCryptoSpareTimersDisabled(t *testing.T) {
	nodes := []scfinvoker.Node{
		{NodeID: "n1", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "n2", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "n3", NodeType: "scf-event", TriggerType: "timer"},
	}
	assignments, err := BuildAssignments([]TaskGroup{{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: []string{"BTC-USDT", "ETH-USDT"}, ExternalSymbols: map[string]string{"BTC-USDT": "BTCUSDT", "ETH-USDT": "ETHUSDT"}}}, nodes, 30)
	require.NoError(t, err)
	require.Len(t, assignments, 3)
	sort.Slice(assignments, func(i, j int) bool { return assignments[i].Enabled && !assignments[j].Enabled })
	require.True(t, assignments[0].Enabled)
	require.False(t, assignments[1].Enabled)
	require.False(t, assignments[2].Enabled)
}

func stockCNExternalSymbols(subjects []string) map[string]string {
	result := make(map[string]string, len(subjects))
	for _, subject := range subjects {
		result[subject] = "sh" + subject[:6]
	}
	return result
}

func TestBuildAssignmentsStableAndBounded(t *testing.T) {
	subjects := make([]string, 0, 61)
	for i := 0; i < 61; i++ {
		subjects = append(subjects, fmt.Sprintf("S%02d-USDT", i))
	}
	externalSymbols := make(map[string]string, len(subjects))
	for _, subject := range subjects {
		externalSymbols[subject] = fmt.Sprintf("S%02dUSDT", len(externalSymbols))
	}
	nodes := []scfinvoker.Node{{NodeID: "n2", Region: "ap-shanghai", NodeType: "scf-event", TriggerType: "timer"}, {NodeID: "n1", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}, {NodeID: "n3", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"}}
	group := TaskGroup{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: subjects, ExternalSymbols: externalSymbols}
	assignments, err := BuildAssignments([]TaskGroup{group}, nodes, 30)
	require.NoError(t, err)
	require.Len(t, assignments, 3)
	for _, assignment := range assignments {
		require.LessOrEqual(t, len(assignment.Subjects), 30)
		require.True(t, assignment.Enabled)
	}
	group.Subjects = append([]string(nil), subjects...)
	assignments2, err := BuildAssignments([]TaskGroup{group}, nodes, 30)
	require.NoError(t, err)
	require.Equal(t, assignments, assignments2)
}

func TestBuildAssignmentsRejectsCapacity(t *testing.T) {
	_, err := BuildAssignments([]TaskGroup{{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: []string{"BTC-USDT", "ETH-USDT"}, ExternalSymbols: map[string]string{"BTC-USDT": "BTCUSDT", "ETH-USDT": "ETHUSDT"}}}, []scfinvoker.Node{{NodeID: "n", NodeType: "scf-event", TriggerType: "timer"}}, 1)
	require.ErrorContains(t, err, "capacity")
}

func TestBuildAssignmentsRejectsMissingExternalSymbolMapping(t *testing.T) {
	_, err := BuildAssignments([]TaskGroup{{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: []string{"BTC-USDT"}}}, []scfinvoker.Node{{NodeID: "n", NodeType: "scf-event", TriggerType: "timer"}}, 30)
	require.ErrorContains(t, err, "external symbol mapping")
}

func TestBuildAssignmentsAllowsUnicodeSubjectNames(t *testing.T) {
	assignments, err := BuildAssignments([]TaskGroup{{
		Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m",
		Subjects: []string{"币安人生-USDT"}, ExternalSymbols: map[string]string{"币安人生-USDT": "BINANCELIFEUSDT"},
	}}, []scfinvoker.Node{{NodeID: "n", NodeType: "scf-event", TriggerType: "timer"}}, 30)
	require.NoError(t, err)
	require.Len(t, assignments, 1)
	if len(assignments) == 1 {
		require.Equal(t, []string{"币安人生-USDT"}, assignments[0].Subjects)
		require.Equal(t, "BINANCELIFEUSDT", assignments[0].ExternalSymbols["币安人生-USDT"])
	}
}

func TestCronForFrequency(t *testing.T) {
	cron, err := CronForFrequency("1m")
	require.NoError(t, err)
	require.Equal(t, "0 * * * * * *", cron)
	_, err = CronForFrequency("2m")
	require.Error(t, err)
}
