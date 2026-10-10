package healthview

import (
	"strings"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/observability"
	pb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
)

func projectPipeline(facts observability.Overview) []*pb.HealthPipelineStage {
	definitions := []struct {
		id         string
		components []string
	}{
		{"collection", []string{"collector"}},
		{"storage", []string{"storage-primary", "storage-node", "storage-view"}},
		{"factor", []string{"factor-mgr"}},
		{"trade", []string{"trade"}},
	}
	producerStage := map[string]string{"collector": "collection", "storage": "storage", "storage_view": "storage", "factor": "factor", "trade": "trade"}
	storageScopes := make(map[string]bool)
	key := func(item observability.DatasetFrequencyStatus) string {
		return item.SpaceID + "\x00" + item.DatasetID + "\x00" + strings.ToLower(item.Freq)
	}
	for _, item := range facts.Datasets {
		if item.Producer == "storage" {
			storageScopes[key(item)] = true
		}
	}
	var out []*pb.HealthPipelineStage
	for _, definition := range definitions {
		stage := &pb.HealthPipelineStage{Id: definition.id, Status: "disabled"}
		expected := false
		var names []string
		for _, id := range definition.components {
			names = append(names, componentName(facts, id))
			for _, service := range facts.Services {
				if service.ServiceName == id && service.Enabled {
					status := domain.HealthStatus(service.Status)
					if !expected || rank(status) > rank(stage.Status) {
						stage.Status = status
					}
					expected = true
				}
			}
		}
		if !facts.TopologyKnown {
			stage.Status = "unknown"
		}
		stage.Name = strings.Join(names, " / ")
		for _, item := range facts.Datasets {
			if producerStage[item.Producer] != definition.id || strings.HasPrefix(item.DatasetID, "dataset_mooxsys_host_") {
				continue
			}
			if item.Producer == "collector" && storageScopes[key(item)] {
				continue
			}
			status := domain.HealthStatus(item.Status)
			stage.Datasets = append(stage.Datasets, &pb.HealthDataset{Producer: item.Producer, SpaceId: item.SpaceID, DatasetId: item.DatasetID, Freq: item.Freq, Status: status, Reason: ChineseReason(item.Reason), RawError: unhealthyRaw(status, item.Reason, ""), InputWatermarkAt: stamp(item.InputWatermarkAt), OutputWatermarkAt: stamp(item.OutputWatermarkAt), LastSuccessAt: stamp(item.LastSuccessAt), LastRunAt: stamp(item.LastRunAt), LastReportedAt: stamp(item.LastReportedAt), LagSeconds: item.LagSeconds})
			if rank(status) > rank(stage.Status) {
				stage.Status = status
			}
		}
		if facts.TopologyKnown && !expected {
			stage.Status = "disabled"
		}
		out = append(out, stage)
	}
	return out
}

func rank(status string) int {
	switch status {
	case "down":
		return 5
	case "degraded":
		return 4
	case "unknown":
		return 3
	case "healthy":
		return 2
	default:
		return 1
	}
}
