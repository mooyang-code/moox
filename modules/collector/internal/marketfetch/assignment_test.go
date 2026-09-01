package marketfetch

import (
	"fmt"
	"sort"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	stocksource "github.com/mooyang-code/moox/modules/collector/internal/sources/stockcn"
	"github.com/stretchr/testify/require"
)

const stockCNTestMeasuredSafeGroupSize = 30

func TestBuildStockCNAssignmentsUsesEveryTimerNodeAndKeepsExistingSubjectsStable(t *testing.T) {
	subjects := []string{"600000.XSHG", "000001.XSHE", "601318.XSHG", "300750.XSHE", "000858.XSHE"}
	externals := stockCNExternalSymbols(subjects)
	nodes := []scfinvoker.Node{
		{NodeID: "node-c", FunctionName: "moox-stock-cn-ap-shanghai-000", Region: "ap-shanghai", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "node-a", FunctionName: "moox-stock-cn-ap-guangzhou-000", Region: "ap-guangzhou", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "node-b", FunctionName: "moox-stock-cn-ap-beijing-000", Region: "ap-beijing", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "worker", FunctionName: "not-a-timer", NodeType: "scf-resident", TriggerType: "http"},
	}
	group := TaskGroup{Provider: "stock_cn_multi", MarketType: "equity", DatasetID: StockCNDatasetID, Frequency: "1m", Subjects: subjects, ExternalSymbols: externals}

	assignments, err := BuildStockCNAssignments(group, nodes, stockCNTestMeasuredSafeGroupSize, "2026-08-29", 3)
	require.NoError(t, err)
	require.Len(t, assignments, 3)
	seen := make(map[string]int, len(subjects))
	before := make(map[string]string, len(subjects))
	for index, assignment := range assignments {
		require.True(t, assignment.Enabled)
		require.Equal(t, index, assignment.GroupID)
		require.Equal(t, StockCNRouteID, assignment.RouteVersion)
		require.Equal(t, "stock_cn_multi", assignment.RouteProvider)
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
	after, err := BuildStockCNAssignments(group, append([]scfinvoker.Node(nil), nodes...), stockCNTestMeasuredSafeGroupSize, "2026-08-29", 3)
	require.NoError(t, err)
	for _, assignment := range after {
		for _, subject := range assignment.Subjects {
			if nodeID, exists := before[subject]; exists {
				require.Equal(t, nodeID, assignment.NodeID, "adding one subject must not move %s", subject)
			}
		}
	}
}

func TestEligibleTimerNodesExcludesIndependentInstrumentSnapshotTimer(t *testing.T) {
	nodes := eligibleTimerNodes([]scfinvoker.Node{
		{NodeID: "kline", NodeType: "scf-event", TriggerType: "timer", Metadata: map[string]any{"function_mode": "kline"}},
		{NodeID: "instrument", NodeType: "scf-event", TriggerType: "timer", Metadata: map[string]any{"function_mode": "instrument_snapshot"}},
		{NodeID: "invoke", NodeType: "scf-event", TriggerType: "invoke", Metadata: map[string]any{"function_mode": "kline"}},
	})

	require.Len(t, nodes, 1)
	require.Equal(t, "kline", nodes[0].NodeID)
}

func TestBuildStockCNAssignmentsRequiresConfiguredNodeCount(t *testing.T) {
	subjects := []string{"600000.XSHG"}
	nodes := []scfinvoker.Node{
		{NodeID: "node-0", FunctionName: "moox-stock-cn-000", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "node-1", FunctionName: "moox-stock-cn-001", NodeType: "scf-event", TriggerType: "timer"},
	}
	_, err := BuildStockCNAssignments(TaskGroup{
		Provider: "stock_cn_multi", MarketType: "equity", DatasetID: StockCNDatasetID, Frequency: "1m",
		Subjects: subjects, ExternalSymbols: stockCNExternalSymbols(subjects),
	}, nodes, stockCNTestMeasuredSafeGroupSize, "2026-08-29", 3)
	require.ErrorContains(t, err, "has 2 nodes; expected 3")
}

func TestBuildStockCNAssignmentsRequiresExplicitPositiveN(t *testing.T) {
	nodes := []scfinvoker.Node{{NodeID: "node-0", FunctionName: "moox-stock-cn-000", NodeType: "scf-event", TriggerType: "timer"}}
	_, err := BuildStockCNAssignments(TaskGroup{
		Provider: "stock_cn_multi", MarketType: "equity", DatasetID: StockCNDatasetID, Frequency: "1m",
		Subjects: []string{"600000.XSHG"}, ExternalSymbols: stockCNExternalSymbols([]string{"600000.XSHG"}),
	}, nodes, 50, "2026-08-29")
	require.ErrorContains(t, err, "explicit positive timer function count")
}

func TestBuildStockCNAssignmentsAllowsStrictlyConvertibleSubjectsWithoutOverrides(t *testing.T) {
	nodes := []scfinvoker.Node{{NodeID: "node-0", FunctionName: "moox-stock-cn-000", NodeType: "scf-event", TriggerType: "timer"}}
	assignments, err := BuildStockCNAssignments(TaskGroup{
		Provider: "stock_cn_multi", MarketType: "equity", DatasetID: StockCNDatasetID, Frequency: "1m",
		Subjects: []string{"600000.XSHG"},
	}, nodes, stockCNTestMeasuredSafeGroupSize, "2026-08-29", 1)
	require.NoError(t, err)
	require.Len(t, assignments, 1)
	require.Empty(t, assignments[0].ExternalSymbols)
}

func TestStockCNStaggeredCronUsesConfiguredFiveToThirtyNineSecondWindow(t *testing.T) {
	require.Equal(t, "5 * * * * * *", stockCNStaggeredCron(0))
	require.Equal(t, "39 * * * * * *", stockCNStaggeredCron(34))
	require.Equal(t, "5 * * * * * *", stockCNStaggeredCron(35))
}

func TestBuildStockCNAssignmentsRejectsMissingPublishedSlot(t *testing.T) {
	subjects := []string{"600000.XSHG"}
	nodes := []scfinvoker.Node{
		{NodeID: "node-0", FunctionName: "moox-stock-cn-000", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "node-2", FunctionName: "moox-stock-cn-002", NodeType: "scf-event", TriggerType: "timer"},
	}
	_, err := BuildStockCNAssignments(TaskGroup{
		Provider: "stock_cn_multi", MarketType: "equity", DatasetID: StockCNDatasetID, Frequency: "1m",
		Subjects: subjects, ExternalSymbols: stockCNExternalSymbols(subjects),
	}, nodes, stockCNTestMeasuredSafeGroupSize, "2026-08-29", 2)
	require.ErrorContains(t, err, "slot")
}

func TestBuildStockCNAssignmentsRejectsSlotMetadataMismatch(t *testing.T) {
	subjects := []string{"600000.XSHG"}
	nodes := []scfinvoker.Node{{
		NodeID: "node-0", FunctionName: "moox-stock-cn-000", NodeType: "scf-event", TriggerType: "timer",
		Metadata: map[string]any{"index": "invalid"},
	}}
	_, err := BuildStockCNAssignments(TaskGroup{
		Provider: "stock_cn_multi", MarketType: "equity", DatasetID: StockCNDatasetID, Frequency: "1m",
		Subjects: subjects, ExternalSymbols: stockCNExternalSymbols(subjects),
	}, nodes, stockCNTestMeasuredSafeGroupSize, "2026-08-29", 1)
	require.ErrorContains(t, err, "metadata index")
}

func TestBuildStockCNAssignmentsFitsApproximateFullMarketIntoTwoHundredGroups(t *testing.T) {
	subjects := make([]string, 0, 5550)
	for index := 0; index < 5550; index++ {
		subjects = append(subjects, fmt.Sprintf("%06d.XSHG", 600000+index))
	}
	nodes := make([]scfinvoker.Node, 0, 200)
	for index := 0; index < 200; index++ {
		nodes = append(nodes, scfinvoker.Node{NodeID: fmt.Sprintf("node-%03d", index), FunctionName: fmt.Sprintf("moox-stock-cn-%03d", index), NodeType: "scf-event", TriggerType: "timer"})
	}
	group := TaskGroup{Provider: "stock_cn_multi", MarketType: "equity", DatasetID: StockCNDatasetID, Frequency: "1m", Subjects: subjects, ExternalSymbols: stockCNExternalSymbols(subjects)}

	assignments, err := BuildStockCNAssignments(group, nodes, stockCNTestMeasuredSafeGroupSize, "2026-08-29", 200)
	require.NoError(t, err)
	require.Len(t, assignments, 200)
	assigned := 0
	before := make(map[string]int, len(subjects))
	for _, assignment := range assignments {
		require.LessOrEqual(t, len(assignment.Subjects), stockCNTestMeasuredSafeGroupSize)
		_, err := buildManagedEnvironment(assignment, nil, stockCNMaxManagedEnvironmentSize)
		require.NoError(t, err)
		for _, subject := range assignment.Subjects {
			before[subject] = assignment.GroupID
		}
		assigned += len(assignment.Subjects)
	}
	require.Equal(t, len(subjects), assigned)

	group.Subjects = append(group.Subjects, "605550.XSHG")
	group.ExternalSymbols["605550.XSHG"] = "sh605550"
	after, err := BuildStockCNAssignments(group, nodes, stockCNTestMeasuredSafeGroupSize, "2026-08-29", 200)
	require.NoError(t, err)
	moved := 0
	for _, assignment := range after {
		for _, subject := range assignment.Subjects {
			if previous, exists := before[subject]; exists && previous != assignment.GroupID {
				moved++
			}
		}
	}
	require.LessOrEqual(t, moved, 1, "one-symbol active subject set growth should move at most one existing subject")
}

func TestBuildStockCNAssignmentsFitsConfigured170GroupsAt40Subjects(t *testing.T) {
	const (
		shanghai = 2314
		shenzhen = 2897
		beijing  = 339
		groups   = 170
		maxSize  = 40
	)
	subjects := make([]string, 0, shanghai+shenzhen+beijing)
	for index := 0; index < shanghai; index++ {
		subjects = append(subjects, fmt.Sprintf("%06d.XSHG", 600000+index))
	}
	for index := 0; index < shenzhen; index++ {
		subjects = append(subjects, fmt.Sprintf("%06d.XSHE", 1+index))
	}
	for index := 0; index < beijing; index++ {
		subjects = append(subjects, fmt.Sprintf("%06d.XBSE", 920000+index))
	}

	regionCounts := map[string]int{
		"ap-beijing":   32,
		"ap-chengdu":   32,
		"ap-guangzhou": 11,
		"ap-shanghai":  32,
		"ap-singapore": 31,
		"ap-tokyo":     32,
	}
	nodes := make([]scfinvoker.Node, 0, groups)
	for region, count := range regionCounts {
		for index := 0; index < count; index++ {
			nodes = append(nodes, scfinvoker.Node{
				NodeID: fmt.Sprintf("%s-%03d", region, index), FunctionName: fmt.Sprintf("moox-stock-cn-%s-%03d", region, index),
				Region: region, NodeType: "scf-event", TriggerType: "timer", Metadata: map[string]any{"index": index},
			})
		}
	}

	assignments, err := BuildStockCNAssignments(TaskGroup{
		Provider: "stock_cn_multi", MarketType: "equity", DatasetID: StockCNDatasetID, Frequency: "1m", Subjects: subjects,
	}, nodes, maxSize, "2026-08-31", groups)
	require.NoError(t, err)
	require.Len(t, assignments, groups)

	seen := make(map[string]int, len(subjects))
	primaryGroups := make(map[string]int)
	primarySubjects := make(map[string]int)
	backupSubjects := make(map[string]int)
	for _, assignment := range assignments {
		require.LessOrEqual(t, len(assignment.Subjects), maxSize)
		require.Len(t, assignment.ProviderChain, 3)
		require.Equal(t, assignment.Provider, assignment.ProviderChain[0])
		environment, envErr := buildManagedEnvironment(assignment, nil, stockCNMaxManagedEnvironmentSize)
		require.NoError(t, envErr)
		require.LessOrEqual(t, environmentBytes(environment), stockCNMaxManagedEnvironmentSize)
		primaryGroups[assignment.Provider]++
		primarySubjects[assignment.Provider] += len(assignment.Subjects)
		for _, provider := range assignment.ProviderChain[1:] {
			backupSubjects[provider] += len(assignment.Subjects)
		}
		for _, subject := range assignment.Subjects {
			seen[subject]++
		}
	}
	require.Len(t, seen, len(subjects))
	for _, subject := range subjects {
		require.Equal(t, 1, seen[subject])
	}
	require.Equal(t, map[string]int{"eastmoney": 57, "sina": 56, "tencent": 57}, primaryGroups)
	// Subject counts follow the stable group assignment, so the group counts
	// are exact while individual group sizes can differ by the hash result.
	require.LessOrEqual(t, maxMapValue(primarySubjects)-minMapValue(primarySubjects), 3*maxSize)
	require.Greater(t, backupSubjects["sina"], 0)
	require.Greater(t, backupSubjects["eastmoney"], 0)
	require.Greater(t, backupSubjects["tencent"], 0)
}

func maxMapValue(values map[string]int) int {
	max := 0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

func minMapValue(values map[string]int) int {
	min := int(^uint(0) >> 1)
	for _, value := range values {
		if value < min {
			min = value
		}
	}
	return min
}

func TestBuildStockCNAssignmentsKeepsPublishedFleetWhenActiveInstrumentSetIsSmall(t *testing.T) {
	subjects := []string{"600000.XSHG", "000001.XSHE"}
	nodes := make([]scfinvoker.Node, 0, 5)
	for index := 0; index < 5; index++ {
		nodes = append(nodes, scfinvoker.Node{NodeID: fmt.Sprintf("node-%d", index), FunctionName: fmt.Sprintf("moox-stock-cn-%03d", index), NodeType: "scf-event", TriggerType: "timer"})
	}
	assignments, err := BuildStockCNAssignments(TaskGroup{
		Provider: "stock_cn_multi", MarketType: "equity", DatasetID: StockCNDatasetID, Frequency: "1m",
		Subjects: subjects, ExternalSymbols: stockCNExternalSymbols(subjects),
	}, nodes, stockCNTestMeasuredSafeGroupSize, "2026-08-29", 5)
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

func TestRequiredStockCNGroupSizeUsesCeilingForConfiguredN(t *testing.T) {
	tests := []struct {
		name   string
		active int
		n      int
		want   int
	}{
		{name: "N200 exact", active: 200, n: 200, want: 1},
		{name: "N200 remainder", active: 201, n: 200, want: 2},
		{name: "other N", active: 15, n: 7, want: 3},
		{name: "empty", active: 0, n: 200, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := requiredStockCNGroupSize(tt.active, tt.n)
			require.NoError(t, err)
			require.Equal(t, tt.want, got)
		})
	}
	_, err := requiredStockCNGroupSize(1, 0)
	require.ErrorContains(t, err, "positive")
}

func stockCNExternalSymbols(subjects []string) map[string]string {
	result := make(map[string]string, len(subjects))
	for _, subject := range subjects {
		result[subject], _ = stocksource.ProviderSymbol(subject)
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

func TestBuildAssignmentsKeepsDistinctMarketSourcesSeparate(t *testing.T) {
	groups := []TaskGroup{
		{Provider: "eastmoney", MarketType: "equity", MarketID: "stock_cn", InstrumentType: "equity", SourceID: "stock_cn_http", DatasetID: "stock_cn_kline", Frequency: "1d", Subjects: []string{"600000.XSHG"}, ExternalSymbols: map[string]string{"600000.XSHG": "sh600000"}},
		{Provider: "tdx", MarketType: "equity", MarketID: "stock_cn", InstrumentType: "equity", SourceID: "normal_7709", DatasetID: "stock_cn_kline", Frequency: "1d", Subjects: []string{"600000.XSHG"}, ExternalSymbols: map[string]string{"600000.XSHG": "sh600000"}},
	}
	nodes := []scfinvoker.Node{
		{NodeID: "n1", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "n2", NodeType: "scf-event", TriggerType: "timer"},
	}
	assignments, err := BuildAssignments(groups, nodes, 30)
	require.NoError(t, err)
	require.Len(t, assignments, 2)
	require.NotEqual(t, assignments[0].AssignmentHash, assignments[1].AssignmentHash)
	require.NotEqual(t, assignments[0].SourceID, assignments[1].SourceID)
}

func TestBuildAssignmentsKeepsSeriesTagsSeparate(t *testing.T) {
	groups := []TaskGroup{
		{Provider: "eastmoney", MarketType: "equity", MarketID: "stock_cn", InstrumentType: "equity", SourceID: "stock_cn_http", SeriesTag: "raw", DatasetID: "stock_cn_kline", Frequency: "1d", Subjects: []string{"600000.XSHG"}, ExternalSymbols: map[string]string{"600000.XSHG": "sh600000"}},
		{Provider: "eastmoney", MarketType: "equity", MarketID: "stock_cn", InstrumentType: "equity", SourceID: "stock_cn_http", SeriesTag: "adjusted", DatasetID: "stock_cn_kline", Frequency: "1d", Subjects: []string{"600000.XSHG"}, ExternalSymbols: map[string]string{"600000.XSHG": "sh600000"}},
	}
	nodes := []scfinvoker.Node{
		{NodeID: "n1", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "n2", NodeType: "scf-event", TriggerType: "timer"},
	}
	assignments, err := BuildAssignments(groups, nodes, 30)
	require.NoError(t, err)
	require.Len(t, assignments, 2)
	require.NotEqual(t, assignments[0].AssignmentHash, assignments[1].AssignmentHash)
	require.NotEqual(t, assignments[0].SeriesTag, assignments[1].SeriesTag)
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
