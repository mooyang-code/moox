package config

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func validDSLYAML() []byte {
	return []byte(`name: strategy_demo
triggers:
  schedule: {cron: "5 * * * *", timezone: UTC}
  event: {name: ViewFactorPeriodReady}
data: {bar: 1h, calendar: crypto_24x7}
rules:
  momentum:
    pool: {udf: spot_symbols, params: {quote_asset: USDT}}
    filter_before: "turnover_20 > 1000000"
    score: "0.6 * pct_rank(return_20) + 0.4 * pct_rank(turnover_20)"
    select: {where: "score >= 0.6", top: 10}
    weight: 0.60
    filter_after: "return_20 > 0"
  signal:
    pool: [BTC-USDT-SPOT, ETH-USDT-SPOT]
    signals:
      entry: "bars[-1].ma20 <= bars[-1].close && bars[0].ma20 > bars[0].close"
      exit: "bars[-1].ma20 >= bars[-1].close && bars[0].ma20 < bars[0].close"
    weight_each: 0.10
`)
}

func TestDSLContract(t *testing.T) {
	dsl, err := Parse(validDSLYAML())
	require.NoError(t, err)
	require.Equal(t, "strategy_demo", dsl.Name)
	require.Equal(t, "1H", dsl.Data.Bar)
	require.Equal(t, "UTC", dsl.Triggers.Schedule.Timezone)
	require.Len(t, dsl.Rules, 2)
	require.Equal(t, "spot_symbols", dsl.Rules["momentum"].Pool.UDF.Name)
	require.Equal(t, "0.60", dsl.Rules["momentum"].Weight)
}

func TestDSLAllowsEitherTrigger(t *testing.T) {
	raw := validDSLYAML()
	raw = []byte("name: only_schedule\ntriggers: {schedule: {cron: '@hourly'}}\ndata: {bar: 1h, calendar: crypto_24x7}\nrules: {r: {pool: [BTC], weight: 1}}\n")
	_, err := Parse(raw)
	require.NoError(t, err)
	raw = []byte("name: only_event\ntriggers: {event: {name: source.ready}}\ndata: {bar: 1h, calendar: crypto_24x7}\nrules: {r: {pool: [BTC], weight: 1}}\n")
	_, err = Parse(raw)
	require.NoError(t, err)
}

func TestDSLRejectsStaticWeightBudgetOverflow(t *testing.T) {
	raw := []byte("name: over\ntriggers: {event: {name: ready}}\ndata: {bar: 1h, calendar: crypto_24x7}\nrules: {a: {pool: [BTC], weight: 0.7}, b: {pool: [ETH], weight: 0.4}}\n")
	_, err := Parse(raw)
	require.ErrorContains(t, err, "weight upper bound exceeds 1")
}

func TestDSLRejectsDynamicWeightEachPool(t *testing.T) {
	raw := []byte("name: dynamic\ntriggers: {event: {name: ready}}\ndata: {bar: 1h, calendar: crypto_24x7}\nrules: {r: {pool: {udf: spot_symbols}, weight_each: 0.1}}\n")
	_, err := Parse(raw)
	require.ErrorContains(t, err, "weight_each requires a fixed pool")
}

func TestDSLRejectsUnsupportedCalendarBarCombination(t *testing.T) {
	raw := []byte("name: stock-hour\ntriggers: {event: {name: ready}}\ndata: {bar: 1h, calendar: cn_stock}\nrules: {r: {pool: [000001.SZ], weight: 1}}\n")
	_, err := Parse(raw)
	require.ErrorContains(t, err, "cn_stock only supports 1d bars")
}

func TestDSLReservesFullPoolForSignalWeightEach(t *testing.T) {
	raw := []byte("name: signal-budget\ntriggers: {event: {name: ready}}\ndata: {bar: 1h, calendar: crypto_24x7}\nrules: {r: {pool: [BTC, ETH], score: close, select: {top: 1}, signals: {entry: close > 0, exit: close < 0}, weight_each: 0.6}}\n")
	_, err := Parse(raw)
	require.ErrorContains(t, err, "weight upper bound exceeds 1")
}

func TestDSLPreservesMinuteFrequencyUnit(t *testing.T) {
	dsl, err := Parse([]byte("name: minute\ntriggers: {event: {name: ready}}\ndata: {bar: 1m, calendar: crypto_24x7}\nrules: {r: {pool: [BTC], weight: 1}}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if dsl.Data.Bar != "1m" {
		t.Fatalf("minute frequency normalized as %q", dsl.Data.Bar)
	}
}

func TestDSLRejectsUnsupportedMonthAndWeekBars(t *testing.T) {
	for _, bar := range []string{"1M", "1w", "1W"} {
		raw := []byte("name: unsupported\ntriggers: {event: {name: ready}}\ndata: {bar: " + bar + ", calendar: crypto_24x7}\nrules: {r: {pool: [BTC], weight: 1}}\n")
		if _, err := Parse(raw); err == nil {
			t.Fatalf("bar %s should be rejected by the v1 calendar contract", bar)
		}
	}
}

func TestDSLRejectsUnknownLegacyAndInvalidFields(t *testing.T) {
	cases := map[string]string{
		"unknown root":     "unknown: true\n",
		"legacy api":       "api_version: moox.strategy/v2\n",
		"legacy kind":      "kind: coin_selection\n",
		"empty rules":      "rules: {}\n",
		"select no score":  "rules: {r: {pool: [BTC], select: {top: 1}, weight: 1}}\n",
		"empty select":     "rules: {r: {pool: [BTC], score: close, select: {}, weight: 1}}\n",
		"top and tail":     "rules: {r: {pool: [BTC], score: close, select: {top: 1, tail: 1}, weight: 1}}\n",
		"weights conflict": "rules: {r: {pool: [BTC], weight: 1, weight_each: 1}}\n",
		"missing signal":   "rules: {r: {pool: [BTC], signals: {entry: close > 0}, weight: 1}}\n",
		"holding signal":   "rules: {r: {pool: [BTC], signals: {entry: close > 0, exit: close < 0}, holding: {bars: 2, offsets: [0]}, weight: 1}}\n",
		"bad root shape":   "- no\n",
		"numeric name":     "name: 123\n",
		"duplicate key":    "name: a\nname: b\n",
		"second document":  "name: a\n---\nname: b\n",
	}
	for name, fragment := range cases {
		t.Run(name, func(t *testing.T) {
			raw := []byte("name: test\ntriggers: {event: {name: ready}}\ndata: {bar: 1h, calendar: crypto_24x7}\n" + fragment)
			_, err := Parse(raw)
			require.Error(t, err)
		})
	}
}

func TestDSLRejectsInvalidPoolAndHolding(t *testing.T) {
	for name, raw := range map[string]string{
		"pool missing":     "name: x\ntriggers: {event: {name: ready}}\ndata: {bar: 1h, calendar: crypto_24x7}\nrules: {r: {weight: 1}}\n",
		"pool duplicate":   "name: x\ntriggers: {event: {name: ready}}\ndata: {bar: 1h, calendar: crypto_24x7}\nrules: {r: {pool: [BTC, BTC], weight: 1}}\n",
		"offset duplicate": "name: x\ntriggers: {event: {name: ready}}\ndata: {bar: 1h, calendar: crypto_24x7}\nrules: {r: {pool: [BTC], score: close, holding: {bars: 2, offsets: [0, 0]}, weight: 1}}\n",
		"offset range":     "name: x\ntriggers: {event: {name: ready}}\ndata: {bar: 1h, calendar: crypto_24x7}\nrules: {r: {pool: [BTC], score: close, holding: {bars: 2, offsets: [2]}, weight: 1}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(raw))
			require.Error(t, err)
		})
	}
}
