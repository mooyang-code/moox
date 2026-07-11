package rpc

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
	"github.com/mooyang-code/moox/packages/marketmanifest"
	"gorm.io/gorm"
)

func TestMarketSchedulePlansCalendarAndInstrumentFromManifest(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:market-schedule?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&domain.TaskInstance{}); err != nil {
		t.Fatal(err)
	}
	if err := repository.MigrateMarketControl(db); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 2, 0, 0, 0, time.UTC)
	manifest := marketmanifest.Manifest{MarketID: "crypto_binance", SpaceID: "crypto_binance", Timezone: "UTC", Exchange: marketmanifest.Exchange{ID: "BINANCE"}, ProductTypes: []string{"spot"}, InstrumentTypes: []string{"spot"}, Providers: []marketmanifest.Provider{{ID: "binance", Quotas: []marketmanifest.Quota{{Scope: "ip", WindowSeconds: 60, Limit: 6000}}}}, Datasets: []marketmanifest.Dataset{{ID: "binance_instruments", Role: "provider_data", Feed: "instrument", ProviderID: "binance"}, {ID: "instruments", Role: "unified_data", Feed: "instrument"}, {ID: "spot_kline", Role: "unified_data", Feed: "kline"}, {ID: "calendar", Role: "unified_data", Feed: "calendar"}}}
	service := &Service{db: db, marketControl: repository.NewMarketControlRepository(db), now: func() time.Time { return now }}
	generation := domain.MarketGeneration{Epoch: 1, Generation: now}
	calendar, err := service.planMarketCalendar(context.Background(), &domain.TaskRule{SpaceID: "crypto_binance", MarketID: "crypto_binance", Feed: "calendar", RuleID: "calendar"}, manifest, generation, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var calendarParams map[string]any
	_ = json.Unmarshal([]byte(calendar[0].TaskParams), &calendarParams)
	if calendarParams["job_type"] != "collect.calendar" || calendarParams["provider_id"] != nil {
		t.Fatalf("calendar params=%v", calendarParams)
	}
	instrument, err := service.planMarketInstrument(context.Background(), &domain.TaskRule{SpaceID: "crypto_binance", MarketID: "crypto_binance", Feed: "instrument", RuleID: "instrument", InstrumentTypes: `["spot"]`}, manifest, generation, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	var instrumentParams map[string]any
	_ = json.Unmarshal([]byte(instrument[0].TaskParams), &instrumentParams)
	if instrumentParams["provider_id"] != "binance" || instrumentParams["quota_lease_id"] == "" || instrumentParams["subject_dataset_ids"] == nil {
		t.Fatalf("instrument params=%v", instrumentParams)
	}
}
