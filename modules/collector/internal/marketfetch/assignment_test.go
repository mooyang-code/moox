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
		{Provider: "eastmoney", MarketType: "equity", MarketID: "stock_cn", InstrumentType: "equity", SourceID: "stock_cn_http", SeriesTag: "eastmoney", DatasetID: "stock_cn_kline", Frequency: "1d", Subjects: []string{"SH.600000"}, ExternalSymbols: map[string]string{"SH.600000": "SH.600000"}},
		{Provider: "tdx", MarketType: "equity", MarketID: "stock_cn", InstrumentType: "equity", SourceID: "normal_7709", SeriesTag: "tdx", DatasetID: "stock_cn_kline", Frequency: "1d", Subjects: []string{"SH.600000"}, ExternalSymbols: map[string]string{"SH.600000": "SH.600000"}},
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

func TestBuildAssignmentsLimitsTDXTimerToOneSubject(t *testing.T) {
	assignments, err := BuildAssignments([]TaskGroup{{
		Provider: "tdx", MarketType: "equity", DatasetID: "stock_cn_kline", Frequency: "1d",
		Subjects: []string{"SH.600000", "SH.600001"}, ExternalSymbols: map[string]string{"SH.600000": "SH.600000", "SH.600001": "SH.600001"},
	}}, []scfinvoker.Node{
		{NodeID: "n1", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "n2", NodeType: "scf-event", TriggerType: "timer"},
	}, 30)
	require.NoError(t, err)
	require.Len(t, assignments, 2)
	for _, assignment := range assignments {
		require.Len(t, assignment.Subjects, 1)
	}
}

func TestBuildAssignmentsRejectsConflictingCanonicalWriters(t *testing.T) {
	groups := []TaskGroup{
		{Provider: "eastmoney", MarketType: "equity", MarketID: "stock_cn", InstrumentType: "equity", SourceID: "stock_cn_http", DatasetID: "stock_cn_kline", Frequency: "1d", Subjects: []string{"SH.600000"}, ExternalSymbols: map[string]string{"SH.600000": "SH.600000"}},
		{Provider: "tdx", MarketType: "equity", MarketID: "stock_cn", InstrumentType: "equity", SourceID: "normal_7709", DatasetID: "stock_cn_kline", Frequency: "1d", Subjects: []string{"SH.600000"}, ExternalSymbols: map[string]string{"SH.600000": "SH.600000"}},
	}
	_, err := BuildAssignments(groups, []scfinvoker.Node{
		{NodeID: "n1", NodeType: "scf-event", TriggerType: "timer"},
		{NodeID: "n2", NodeType: "scf-event", TriggerType: "timer"},
	}, 30)
	require.ErrorContains(t, err, "conflicting sources")
}

func TestBuildAssignmentsKeepsSeriesTagsSeparate(t *testing.T) {
	groups := []TaskGroup{
		{Provider: "eastmoney", MarketType: "equity", MarketID: "stock_cn", InstrumentType: "equity", SourceID: "stock_cn_http", SeriesTag: "raw", DatasetID: "stock_cn_kline", Frequency: "1d", Subjects: []string{"SH.600000"}, ExternalSymbols: map[string]string{"SH.600000": "SH.600000"}},
		{Provider: "eastmoney", MarketType: "equity", MarketID: "stock_cn", InstrumentType: "equity", SourceID: "stock_cn_http", SeriesTag: "adjusted", DatasetID: "stock_cn_kline", Frequency: "1d", Subjects: []string{"SH.600000"}, ExternalSymbols: map[string]string{"SH.600000": "SH.600000"}},
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
	for _, test := range []struct {
		frequency string
		cron      string
	}{
		{frequency: "1w", cron: "0 0 0 * * 1 *"},
		{frequency: "1M", cron: "0 0 0 1 * * *"},
		{frequency: "1H", cron: "0 0 * * * * *"},
	} {
		cron, err := CronForFrequency(test.frequency)
		require.NoError(t, err)
		require.Equal(t, test.cron, cron)
	}
}

func TestCronForMarketFrequencyUsesClosedUSMonthlyBar(t *testing.T) {
	cron, err := CronForMarketFrequency("stock_us", "equity", "1M")
	require.NoError(t, err)
	require.Equal(t, "0 0 8 1 * * *", cron)

	cron, err = CronForMarketFrequency("stock_cn", "equity", "1M")
	require.NoError(t, err)
	require.Equal(t, "0 0 0 1 * * *", cron)
}

func TestBuildAssignmentsUsesMarketAwareMonthlyCron(t *testing.T) {
	assignments, err := BuildAssignments([]TaskGroup{{
		Provider: "eastmoney", MarketType: "equity", MarketID: "stock_us", InstrumentType: "equity",
		SourceID: "stock_us_http", DatasetID: "stock_us_kline", Frequency: "1M",
		Subjects: []string{"AAPL"}, ExternalSymbols: map[string]string{"AAPL": "AAPL"},
	}}, []scfinvoker.Node{{NodeID: "n1", NodeType: "scf-event", TriggerType: "timer"}}, 30)
	require.NoError(t, err)
	require.Len(t, assignments, 1)
	require.Equal(t, "0 0 8 1 * * *", assignments[0].Cron)
}
