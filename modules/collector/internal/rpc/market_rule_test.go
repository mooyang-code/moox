package rpc

import (
	"testing"

	pb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestMarketRuleUsesLogicalDemandWithoutProviderAlias(t *testing.T) {
	schedule, _ := structpb.NewStruct(map[string]any{"interval": "1h", "timezone": "UTC"})
	rule := normalizeTaskRule(fromPBRule(&pb.TaskRule{SpaceId: "crypto_binance", RuleId: "hourly", MarketId: "crypto_binance", Feed: "kline", InstrumentTypes: []string{"spot"}, Frequencies: []string{"1h"}, HistoryStart: "2026-01-01T00:00:00Z", ScheduleSpec: schedule, Enabled: boolPtr(true)}))
	if err := validateTaskRule(rule); err != nil {
		t.Fatal(err)
	}
	if rule.Exchange != "" || rule.MarketID != "crypto_binance" {
		t.Fatalf("rule=%+v", rule)
	}
	roundTrip := toPBRule(rule)
	if roundTrip.GetMarketId() != "crypto_binance" || len(roundTrip.GetFrequencies()) != 1 || roundTrip.GetScheduleSpec().AsMap()["interval"] != "1h" {
		t.Fatalf("roundtrip=%+v", roundTrip)
	}
}
func boolPtr(value bool) *bool { return &value }
