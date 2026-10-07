package marketfetch

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/modules/collector/internal/scfinvoker"
	"github.com/stretchr/testify/require"
)

func TestEligibleTimerNodesIncludesAllMarketTimerNodes(t *testing.T) {
	nodes := eligibleTimerNodes([]scfinvoker.Node{
		{NodeID: "kline", NodeType: "scf-event", TriggerType: "timer", Metadata: map[string]any{"function_mode": "kline"}},
		{NodeID: "instrument", NodeType: "scf-event", TriggerType: "timer", Metadata: map[string]any{"function_mode": "instrument_snapshot"}},
		{NodeID: "invoke", NodeType: "scf-event", TriggerType: "invoke", Metadata: map[string]any{"function_mode": "kline"}},
	})

	require.Len(t, nodes, 2)
	require.Equal(t, "kline", nodes[0].NodeID)
	require.Equal(t, "instrument", nodes[1].NodeID)
}

func TestStockCNStaggeredCronUsesConfiguredFiveToThirtyNineSecondWindow(t *testing.T) {
	require.Equal(t, "5 * * * * * *", stockCNStaggeredCron(0))
	require.Equal(t, "39 * * * * * *", stockCNStaggeredCron(34))
	require.Equal(t, "5 * * * * * *", stockCNStaggeredCron(35))
}

func TestBuildAssignmentsPinsPriorityCryptoSubjectsFirst(t *testing.T) {
	subjects := []string{"AAA-USDT", "BTC-USDT", "ETH-USDT", "ZZZ-USDT"}
	externals := map[string]string{"AAA-USDT": "AAAUSDT", "BTC-USDT": "BTCUSDT", "ETH-USDT": "ETHUSDT", "ZZZ-USDT": "ZZZUSDT"}
	nodes := []scfinvoker.Node{
		{NodeID: "n1", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "n2", NodeType: "scf-event", TriggerType: "timer"},
	}
	assignments, err := BuildAssignments([]TaskGroup{{
		Provider: "binance", MarketType: "spot", MarketID: "crypto", DatasetID: "bars", Frequency: "1m",
		Subjects: subjects, ExternalSymbols: externals,
	}}, nodes, 2)
	require.NoError(t, err)
	byGroup := map[int][]string{}
	for _, assignment := range assignments {
		if assignment.Enabled {
			byGroup[assignment.GroupID] = assignment.Subjects
		}
	}
	require.Equal(t, []string{"BTC-USDT", "ETH-USDT"}, byGroup[0])
	require.Equal(t, []string{"AAA-USDT", "ZZZ-USDT"}, byGroup[1])
}

func TestBuildAssignmentsGivesPriorityCryptoADedicatedTimerWhenSpare(t *testing.T) {
	subjects := make([]string, 0, 41)
	externals := make(map[string]string, 41)
	subjects = append(subjects, "AAA-USDT", "BTC-USDT", "ETH-USDT")
	externals["AAA-USDT"] = "AAAUSDT"
	externals["BTC-USDT"] = "BTCUSDT"
	externals["ETH-USDT"] = "ETHUSDT"
	for i := 0; i < 38; i++ {
		subject := fmt.Sprintf("T%02d-USDT", i)
		subjects = append(subjects, subject)
		externals[subject] = strings.ReplaceAll(subject, "-", "")
	}
	nodes := make([]scfinvoker.Node, 0, 4)
	for i := 0; i < 4; i++ {
		nodes = append(nodes, scfinvoker.Node{NodeID: fmt.Sprintf("n%d", i), NodeType: "scf-event", TriggerType: "timer"})
	}
	assignments, err := BuildAssignments([]TaskGroup{{
		Provider: "binance", MarketType: "spot", MarketID: "crypto", DatasetID: "bars", Frequency: "1m",
		Subjects: subjects, ExternalSymbols: externals,
	}}, nodes, 30)
	require.NoError(t, err)
	byGroup := map[int][]string{}
	enabled := 0
	for _, assignment := range assignments {
		if !assignment.Enabled {
			continue
		}
		enabled++
		byGroup[assignment.GroupID] = assignment.Subjects
	}
	require.Equal(t, 4, enabled)
	require.Equal(t, []string{"BTC-USDT"}, byGroup[0])
	require.Equal(t, []string{"ETH-USDT"}, byGroup[1])
	require.NotContains(t, byGroup[2], "BTC-USDT")
	require.NotContains(t, byGroup[2], "ETH-USDT")
	require.Contains(t, byGroup[2], "AAA-USDT")
	require.NotContains(t, byGroup[3], "BTC-USDT")
	require.NotContains(t, byGroup[3], "ETH-USDT")
}

func TestBuildAssignmentsPrefersMinuteDedicatedTimersOverHourly(t *testing.T) {
	subjects := make([]string, 0, 41)
	externals := make(map[string]string, 41)
	subjects = append(subjects, "AAA-USDT", "BTC-USDT", "ETH-USDT")
	externals["AAA-USDT"] = "AAAUSDT"
	externals["BTC-USDT"] = "BTCUSDT"
	externals["ETH-USDT"] = "ETHUSDT"
	for i := 0; i < 38; i++ {
		subject := fmt.Sprintf("T%02d-USDT", i)
		subjects = append(subjects, subject)
		externals[subject] = strings.ReplaceAll(subject, "-", "")
	}
	nodes := make([]scfinvoker.Node, 0, 5)
	for i := 0; i < 5; i++ {
		nodes = append(nodes, scfinvoker.Node{NodeID: fmt.Sprintf("n%d", i), NodeType: "scf-event", TriggerType: "timer"})
	}
	assignments, err := BuildAssignments([]TaskGroup{
		{Provider: "binance", MarketType: "spot", MarketID: "crypto", DatasetID: "bars_1h", Frequency: "1h", Subjects: subjects, ExternalSymbols: externals},
		{Provider: "binance", MarketType: "spot", MarketID: "crypto", DatasetID: "bars_1m", Frequency: "1m", Subjects: subjects, ExternalSymbols: externals},
	}, nodes, 30)
	require.NoError(t, err)
	minuteSolos := 0
	hourSolos := 0
	for _, assignment := range assignments {
		if !assignment.Enabled || len(assignment.Subjects) != 1 {
			continue
		}
		switch assignment.Frequency {
		case "1m":
			minuteSolos++
			require.Equal(t, "BTC-USDT", assignment.Subjects[0])
		case "1h":
			hourSolos++
		}
	}
	require.Equal(t, 1, minuteSolos, "the single spare timer must isolate 1m BTC, not 1h")
	require.Zero(t, hourSolos)
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

func TestSelectCryptoGroupsForCapacityKeepsMinuteBars(t *testing.T) {
	subjects := make([]string, 0, 80)
	externals := make(map[string]string, 80)
	for i := 0; i < 80; i++ {
		subject := fmt.Sprintf("S%02d-USDT", i)
		subjects = append(subjects, subject)
		externals[subject] = fmt.Sprintf("S%02dUSDT", i)
	}
	groups := []TaskGroup{
		{Provider: "binance", MarketType: "spot", DatasetID: "dataset_binance_spot_kline_1h", Frequency: "1H", Subjects: subjects, ExternalSymbols: externals},
		{Provider: "binance", MarketType: "spot", DatasetID: "dataset_binance_kline_1m", Frequency: "1m", Subjects: subjects, ExternalSymbols: externals},
		{Provider: "binance", MarketType: "swap", DatasetID: "dataset_binance_kline_1m", Frequency: "1m", Subjects: subjects, ExternalSymbols: externals},
		{Provider: "binance", MarketType: "swap", DatasetID: "dataset_binance_swap_kline_1h", Frequency: "1H", Subjects: subjects, ExternalSymbols: externals},
	}
	nodes := make([]scfinvoker.Node, 0, 4)
	for i := 0; i < 4; i++ {
		nodes = append(nodes, scfinvoker.Node{NodeID: fmt.Sprintf("n%d", i), Region: "ap-nanjing", NodeType: "scf-event", TriggerType: "timer"})
	}
	selected, deferred, err := selectCryptoGroupsForCapacity(groups, nodes, 40)
	require.NoError(t, err)
	require.Len(t, selected, 2)
	require.Len(t, deferred, 2)
	for _, group := range selected {
		require.Equal(t, "1m", group.Frequency)
	}
	for _, group := range deferred {
		require.Equal(t, "1H", group.Frequency)
	}
}

func TestSelectCryptoGroupsForCapacityKeepsBinanceCryptoInOverseasRegion(t *testing.T) {
	subjects := make([]string, 0, 80)
	externals := make(map[string]string, 80)
	for i := 0; i < 80; i++ {
		subject := fmt.Sprintf("S%02d-USDT", i)
		subjects = append(subjects, subject)
		externals[subject] = fmt.Sprintf("S%02dUSDT", i)
	}
	groups := []TaskGroup{
		{Provider: "binance", MarketType: "spot", DatasetID: "dataset_binance_spot_kline_1h", Frequency: "1H", Subjects: subjects, ExternalSymbols: externals},
		{Provider: "binance", MarketType: "spot", DatasetID: "dataset_binance_kline_1m", Frequency: "1m", Subjects: subjects, ExternalSymbols: externals},
		{Provider: "binance", MarketType: "swap", DatasetID: "dataset_binance_kline_1m", Frequency: "1m", Subjects: subjects, ExternalSymbols: externals},
		{Provider: "binance", MarketType: "swap", DatasetID: "dataset_binance_swap_kline_1h", Frequency: "1H", Subjects: subjects, ExternalSymbols: externals},
	}
	nodes := []scfinvoker.Node{
		{NodeID: "hk-0", Region: "ap-hongkong", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "hk-1", Region: "ap-hongkong", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "hk-2", Region: "ap-hongkong", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "hk-3", Region: "ap-hongkong", NodeType: "scf-event", TriggerType: "timer"},
	}
	selected, deferred, err := selectCryptoGroupsForCapacity(groups, nodes, 40)
	require.NoError(t, err)
	require.Len(t, selected, 2)
	require.Len(t, deferred, 2)
	selectedKeys := make([]string, 0, len(selected))
	for _, group := range selected {
		selectedKeys = append(selectedKeys, group.MarketType+"/"+group.Frequency)
	}
	require.ElementsMatch(t, []string{"swap/1m", "spot/1m"}, selectedKeys)
	for _, group := range deferred {
		require.Equal(t, "1H", group.Frequency)
	}
}

func TestBuildAssignmentsPinsBinanceCryptoToOverseasRegions(t *testing.T) {
	subjects := []string{"AAA-USDT", "BTC-USDT"}
	externals := map[string]string{"AAA-USDT": "AAAUSDT", "BTC-USDT": "BTCUSDT"}
	nodes := []scfinvoker.Node{
		{NodeID: "nj-0", FunctionName: "fn-nj-0", Region: "ap-nanjing", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "hk-0", FunctionName: "fn-hk-0", Region: "ap-hongkong", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "hk-1", FunctionName: "fn-hk-1", Region: "ap-hongkong", NodeType: "scf-event", TriggerType: "timer"},
	}
	assignments, err := BuildAssignments([]TaskGroup{
		{Provider: "binance", MarketType: "spot", MarketID: "crypto", DatasetID: "dataset_binance_kline_1m", Frequency: "1m", Subjects: subjects, ExternalSymbols: externals},
		{Provider: "binance", MarketType: "swap", MarketID: "crypto", DatasetID: "dataset_binance_kline_1m", Frequency: "1m", Subjects: subjects, ExternalSymbols: externals},
	}, nodes, 30)
	require.NoError(t, err)
	byMarket := map[string]NodeAssignment{}
	for _, assignment := range assignments {
		if !assignment.Enabled {
			continue
		}
		byMarket[assignment.MarketType] = assignment
	}
	require.Equal(t, "ap-hongkong", byMarket["swap"].Region)
	require.Equal(t, "ap-hongkong", byMarket["spot"].Region)
	require.NotEqual(t, "nj-0", byMarket["swap"].NodeID)
	require.NotEqual(t, "nj-0", byMarket["spot"].NodeID)
}

func TestRequiresOverseasEgressPinsBinanceCryptoSpotAndSwap(t *testing.T) {
	for _, marketType := range []string{"spot", "swap"} {
		require.True(t, requiresOverseasEgress(TaskGroup{
			Provider: "binance", MarketID: "crypto", MarketType: marketType,
			DatasetID: "dataset_binance_kline_1m",
		}), marketType)
	}
	require.False(t, requiresOverseasEgress(TaskGroup{Provider: "eastmoney", MarketID: "stockcn", MarketType: "equity"}))
}

func TestBuildAssignmentsRejectsOverseasCapacity(t *testing.T) {
	subjects := []string{"AAA-USDT", "BTC-USDT"}
	externals := map[string]string{"AAA-USDT": "AAAUSDT", "BTC-USDT": "BTCUSDT"}
	_, err := BuildAssignments([]TaskGroup{
		{Provider: "binance", MarketType: "swap", MarketID: "crypto", DatasetID: "dataset_binance_kline_1m", Frequency: "1m", Subjects: subjects, ExternalSymbols: externals},
	}, []scfinvoker.Node{
		{NodeID: "nj-0", Region: "ap-nanjing", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "hk-0", Region: "ap-hongkong", NodeType: "scf-event", TriggerType: "timer"},
	}, 1)
	require.ErrorContains(t, err, "overseas")
}

func TestBuildAssignmentsRejectsMissingExternalSymbolMapping(t *testing.T) {
	_, err := BuildAssignments([]TaskGroup{{Provider: "binance", MarketType: "spot", DatasetID: "bars", Frequency: "1m", Subjects: []string{"BTC-USDT"}}}, []scfinvoker.Node{{NodeID: "n", NodeType: "scf-event", TriggerType: "timer"}}, 30)
	require.ErrorContains(t, err, "external symbol mapping")
}

func TestBuildAssignmentsKeepsDistinctMarketSourcesSeparate(t *testing.T) {
	groups := []TaskGroup{
		{Provider: "eastmoney", MarketType: "equity", MarketID: "stockcn", InstrumentType: "equity", SourceID: "stockcn_http", DatasetID: "dataset_stockcn_equity_kline", Frequency: "1d", Subjects: []string{"600000.XSHG"}, ExternalSymbols: map[string]string{"600000.XSHG": "sh600000"}},
		{Provider: "tdx", MarketType: "equity", MarketID: "stockcn", InstrumentType: "equity", SourceID: "normal_7709", DatasetID: "dataset_stockcn_equity_kline", Frequency: "1d", Subjects: []string{"600000.XSHG"}, ExternalSymbols: map[string]string{"600000.XSHG": "sh600000"}},
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
		{Provider: "eastmoney", MarketType: "equity", MarketID: "stockcn", InstrumentType: "equity", SourceID: "stockcn_http", SeriesTag: "raw", DatasetID: "dataset_stockcn_equity_kline", Frequency: "1d", Subjects: []string{"600000.XSHG"}, ExternalSymbols: map[string]string{"600000.XSHG": "sh600000"}},
		{Provider: "eastmoney", MarketType: "equity", MarketID: "stockcn", InstrumentType: "equity", SourceID: "stockcn_http", SeriesTag: "adjusted", DatasetID: "dataset_stockcn_equity_kline", Frequency: "1d", Subjects: []string{"600000.XSHG"}, ExternalSymbols: map[string]string{"600000.XSHG": "sh600000"}},
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
	cron, err = CronForFrequency("1M")
	require.NoError(t, err)
	require.Equal(t, "0 0 0 1 * * *", cron)
	_, err = CronForFrequency("2m")
	require.Error(t, err)
}

func TestAssignmentCronStaggersCryptoHourlyShards(t *testing.T) {
	group := TaskGroup{MarketID: "crypto", Frequency: "1h"}
	for id, want := range map[int]string{0: "0 0 * * * * *", 1: "0 1 * * * * *", 9: "0 9 * * * * *", 10: "0 0 * * * * *"} {
		if got := assignmentCron(group, id); got != want {
			t.Fatalf("group %d cron=%q, want %q", id, got, want)
		}
	}
	if got := assignmentCron(TaskGroup{MarketID: "crypto", Frequency: "1m"}, 3); got != "0 * * * * * *" {
		t.Fatalf("minute cron=%q", got)
	}
}
