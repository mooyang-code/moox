package performance

import (
	"context"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

func testAggregator(t *testing.T) *Aggregator {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	return &Aggregator{DB: db}
}

func TestWritePointIsIdempotentAndSourceScoped(t *testing.T) {
	a := testAggregator(t)
	in := Input{BindingID: "b1", Source: "paper", PointTime: time.Date(2026, 7, 11, 10, 0, 0, 0, time.UTC), NAV: "1.01", CumulativeReturn: "0.01", Drawdown: "0", GrossExposure: "1", NetExposure: "1", Turnover: "0.2", Fees: "0.1", DataRevision: "paper:1"}
	if err := a.WritePoint(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	in.NAV = "1.02"
	if err := a.WritePoint(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	var points, daily int64
	a.DB.Table("t_strategy_performance_points").Where("c_binding_id=? AND c_performance_source=?", "b1", "paper").Count(&points)
	a.DB.Table("t_strategy_performance_daily").Where("c_binding_id=? AND c_performance_source=?", "b1", "paper").Count(&daily)
	if points != 1 || daily != 1 {
		t.Fatalf("points=%d daily=%d", points, daily)
	}
	var nav string
	a.DB.Table("t_strategy_performance_points").Select("c_nav").Where("c_binding_id=?", "b1").Scan(&nav)
	if nav != "1.02" {
		t.Fatalf("nav=%q", nav)
	}
}
