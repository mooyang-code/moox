package rpc

import (
	"strings"
	"testing"
	"time"

	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

func TestGetObservabilityOverviewSupportsAllSpacesAndMapsAllSections(t *testing.T) {
	svc := New(store.NewRepositories(nil), Options{})
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	svc.observabilityOverview = &monitorobservability.Builder{Now: func() time.Time { return now }}
	rsp, err := svc.GetObservabilityOverview(t.Context(), &monitorpb.GetObservabilityOverviewReq{})
	if err != nil {
		t.Fatal(err)
	}
	if rsp.GetRetInfo().GetCode() != 0 || rsp.GetOverview().GetGeneratedAt() != now.Format(time.RFC3339Nano) {
		t.Fatalf("overview response = %+v", rsp)
	}
	if rsp.GetOverview().GetServices() == nil || rsp.GetOverview().GetHosts() == nil || rsp.GetOverview().GetDatasets() == nil ||
		rsp.GetOverview().GetBusinessChecks() == nil {
		t.Fatalf("overview sections are not initialized: %+v", rsp.GetOverview())
	}
}

func TestOverviewToPBPreservesDatasetLagAndBusinessReason(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	got := overviewToPB(monitorobservability.Overview{
		GeneratedAt: now,
		Datasets: []monitorobservability.DatasetFrequencyStatus{{
			Producer: "collector", SpaceID: "moox_system", DatasetID: "bars", Freq: "1m",
			Status: "stale", Reason: "输出水位已落后 1 分钟，最新输出时间 2026-07-28 11:59:00 UTC（检查时间 2026-07-28 12:00:00 UTC）", OutputWatermarkAt: now.Add(-time.Minute), LagSeconds: 60,
		}},
		BusinessChecks: []monitorobservability.BusinessStatus{{
			Kind: "canary", Module: "monitor", Status: "down", Reason: "market jump", LastCheckedAt: now,
		}},
	})
	if got.GetDatasets()[0].GetLagSeconds() != 60 || !strings.Contains(got.GetDatasets()[0].GetReason(), "输出水位已落后") {
		t.Fatalf("dataset = %+v", got.GetDatasets()[0])
	}
	if got.GetBusinessChecks()[0].GetReason() != "market jump" {
		t.Fatalf("business = %+v", got.GetBusinessChecks()[0])
	}
}
