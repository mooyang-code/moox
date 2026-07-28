package rpc

import (
	"testing"
	"time"

	monitorobservability "github.com/mooyang-code/moox/modules/monitor/internal/observability"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

func TestGetObservabilityOverviewValidatesSpaceAndMapsAllSections(t *testing.T) {
	svc := New(store.NewRepositories(nil), Options{})
	invalidRsp, err := svc.GetObservabilityOverview(t.Context(), &monitorpb.GetObservabilityOverviewReq{})
	if err != nil {
		t.Fatal(err)
	}
	if invalidRsp.GetRetInfo().GetCode() == 0 {
		t.Fatalf("empty space response = %+v", invalidRsp)
	}

	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	svc.observabilityOverview = &monitorobservability.Builder{Now: func() time.Time { return now }}
	rsp, err := svc.GetObservabilityOverview(t.Context(), &monitorpb.GetObservabilityOverviewReq{SpaceId: "moox_system"})
	if err != nil {
		t.Fatal(err)
	}
	if rsp.GetRetInfo().GetCode() != 0 || rsp.GetOverview().GetGeneratedAt() != now.Format(time.RFC3339Nano) {
		t.Fatalf("overview response = %+v", rsp)
	}
	if rsp.GetOverview().GetScf() == nil || rsp.GetOverview().GetServices() == nil ||
		rsp.GetOverview().GetHosts() == nil || rsp.GetOverview().GetDatasets() == nil ||
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
			Status: "stale", Reason: "watermark stale", OutputWatermarkAt: now.Add(-time.Minute), LagSeconds: 60,
		}},
		BusinessChecks: []monitorobservability.BusinessStatus{{
			Kind: "canary", Module: "monitor", Status: "down", Reason: "market jump", LastCheckedAt: now,
		}},
	})
	if got.GetDatasets()[0].GetLagSeconds() != 60 || got.GetDatasets()[0].GetReason() != "watermark stale" {
		t.Fatalf("dataset = %+v", got.GetDatasets()[0])
	}
	if got.GetBusinessChecks()[0].GetReason() != "market jump" {
		t.Fatalf("business = %+v", got.GetBusinessChecks()[0])
	}
}
