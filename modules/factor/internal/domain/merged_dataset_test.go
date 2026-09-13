package domain

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMergedDatasetDefinitionRejectsFrequencyMismatch(t *testing.T) {
	def := validSpotSwapMergedDataset()
	def.Sources[1].Frequency = "5m"
	require.ErrorContains(t, ValidateMergedDataset(def), "frequency")
}

func TestMergedDatasetDefinitionRejectsTimeBoundaryMismatch(t *testing.T) {
	def := validSpotSwapMergedDataset()
	def.Sources[1].PeriodBoundary = "open"
	require.ErrorContains(t, ValidateMergedDataset(def), "period boundary")
}

func TestMergedDatasetDefinitionRejectsFieldPrefixConflict(t *testing.T) {
	def := validSpotSwapMergedDataset()
	def.Sources[1].Fields = append(def.Sources[1].Fields, "open")
	def.FieldMappings = append(def.FieldMappings, FieldMapping{
		SourceDatasetID: def.Sources[0].DatasetID, SourceField: "close",
		TargetField: MappedSourceField(def.Sources[1].DatasetID, "open"),
	})
	require.ErrorContains(t, ValidateMergedDataset(def), "field")
}

func TestMergedDatasetDefinitionRejectsSourceChangeAfterEnable(t *testing.T) {
	current := validSpotSwapMergedDataset()
	current.Enabled = true
	next := current
	next.Sources = append([]SourceDatasetRef(nil), current.Sources[:1]...)
	require.ErrorContains(t, ValidateMergedDatasetUpdate(current, next), "input semantics")
}

func TestMergedDatasetDefinitionCacheIdentityIncludesDatasetID(t *testing.T) {
	left := CacheIdentity("mdataset_binance_kline_1m", "schema-ohlcv")
	right := CacheIdentity("mdataset_other_kline_1m", "schema-ohlcv")
	require.NotEqual(t, left, right)
	require.Equal(t, left, CacheIdentity("mdataset_binance_kline_1m", "schema-ohlcv"))
	require.NotEqual(t, left, CacheIdentity("mdataset_binance_kline_1m", "schema-other"))
}

func TestMergedDatasetDefinitionMapsPrefixedSourceFields(t *testing.T) {
	require.Equal(t, "dataset_binance_spot_kline_1m__close", MappedSourceField("dataset_binance_spot_kline_1m", "close"))
	require.Equal(t, "subject_id", MappedSourceField("dataset_binance_spot_kline_1m", "subject_id"))
}

func validSpotSwapMergedDataset() MergedDataset {
	spot := "dataset_binance_spot_kline_1m"
	swap := "dataset_binance_swap_kline_1m"
	fields := []string{"open", "high", "low", "close", "volume", "quote_volume", "trade_num"}
	def := MergedDataset{
		DatasetID: "mdataset_binance_kline_1m",
		SpaceID:   "crypto",
		Frequency: "1m",
		KeyContract: KeyContract{
			SubjectID: "subject_id", Frequency: "frequency", PeriodTime: "period_time", SeriesTag: "series_tag",
			PeriodBoundary: "close",
		},
		ObjectSet: []string{"BTC-USDT", "ETH-USDT"},
		Sources: []SourceDatasetRef{
			{DatasetID: spot, Frequency: "1m", PeriodBoundary: "close", Fields: append([]string(nil), fields...)},
			{DatasetID: swap, Frequency: "1m", PeriodBoundary: "close", Fields: append([]string(nil), fields...)},
		},
		MergeMode: MergeModeSystem,
	}
	for _, source := range def.Sources {
		for _, field := range source.Fields {
			def.FieldMappings = append(def.FieldMappings, FieldMapping{
				SourceDatasetID: source.DatasetID, SourceField: field, TargetField: MappedSourceField(source.DatasetID, field),
			})
		}
	}
	return def
}

func TestMergedDatasetDefinitionRequiresValidSpotSwapExample(t *testing.T) {
	require.NoError(t, ValidateMergedDataset(validSpotSwapMergedDataset()))
	require.False(t, strings.HasPrefix(MappedSourceField("dataset_binance_spot_kline_1m", "subject_id"), "dataset_"))
}
