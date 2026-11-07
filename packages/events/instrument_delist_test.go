package events

import (
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/storagepb"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestInstrumentDelistScheduledRoundTripAndValidation(t *testing.T) {
	registry, err := DefaultRegistry()
	require.NoError(t, err)
	seen := time.Date(2026, 11, 4, 8, 0, 0, 0, time.UTC)
	delistAt := seen.Add(7 * 24 * time.Hour)
	payload := &storagepb.InstrumentDelistScheduled{
		Exchange: "binance", Market: DelistMarketSpot, ExchangeSymbol: "FOOUSDT",
		DelistAt: timestamppb.New(delistAt), Source: DelistSourceBinanceSpotSchedule,
		FirstSeenAt: timestamppb.New(seen), Scope: DelistScopeAll,
	}
	subject := InstrumentDelistSubjectID("binance", DelistMarketSpot, "FOOUSDT")
	eventID := InstrumentDelistEventID("binance", DelistMarketSpot, "FOOUSDT", DelistScopeAll, delistAt)
	require.Equal(t, eventID, InstrumentDelistEventID("binance", DelistMarketSpot, "FOOUSDT", DelistScopeAll, delistAt.In(time.FixedZone("x", 3600))))
	require.NotEqual(t, eventID, InstrumentDelistEventID("binance", DelistMarketSpot, "FOOUSDT", DelistScopeAll, delistAt.Add(time.Second)))
	require.NotEqual(t, eventID, InstrumentDelistEventID("binance", DelistMarketSpot, "FOOUSDT", DelistScopePaperOnly, delistAt), "范围不同是不同的事件")
	opts := PublishOptions{EventID: eventID, OccurredAt: seen, SpaceID: DelistSystemSpaceID, SubjectID: subject}

	raw, err := registry.MarshalMessage(InstrumentDelistScheduled, payload, opts)
	require.NoError(t, err)
	message, err := registry.UnmarshalMessage(raw)
	require.NoError(t, err)
	natsSubject, err := registry.SubjectForMessage(message)
	require.NoError(t, err)
	require.Contains(t, natsSubject, "moox.event.collector.instrument.delist_scheduled.v1.")
	require.Equal(t, "MOOX_COLLECTOR", InstrumentDelistScheduled.Stream())

	for name, mutate := range map[string]func(*storagepb.InstrumentDelistScheduled){
		"subject mismatch": func(p *storagepb.InstrumentDelistScheduled) { p.ExchangeSymbol = "BARUSDT" },
		"upper exchange":   func(p *storagepb.InstrumentDelistScheduled) { p.Exchange = "BINANCE" },
		"bad market":       func(p *storagepb.InstrumentDelistScheduled) { p.Market = "FUTURES" },
		"no delist time":   func(p *storagepb.InstrumentDelistScheduled) { p.DelistAt = nil },
		"no first seen":    func(p *storagepb.InstrumentDelistScheduled) { p.FirstSeenAt = nil },
		"bad source":       func(p *storagepb.InstrumentDelistScheduled) { p.Source = "OTHER" },
		"bad scope":        func(p *storagepb.InstrumentDelistScheduled) { p.Scope = "" },
	} {
		broken := proto.Clone(payload).(*storagepb.InstrumentDelistScheduled)
		mutate(broken)
		_, err := registry.Encode(InstrumentDelistScheduled, broken, opts)
		require.Error(t, err, name)
	}

	// 交易所代码必须是大写：subject 也一起改成小写，这样只有大写校验能拦住它（不是靠 subject 对不上）。
	lower := proto.Clone(payload).(*storagepb.InstrumentDelistScheduled)
	lower.ExchangeSymbol = "foousdt"
	lowerOpts := opts
	lowerOpts.SubjectID = InstrumentDelistSubjectID("binance", DelistMarketSpot, "foousdt")
	_, err = registry.Encode(InstrumentDelistScheduled, lower, lowerOpts)
	require.ErrorContains(t, err, "upper case")
}
