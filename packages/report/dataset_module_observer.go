package report

import (
	"fmt"
)

// DatasetRunObserver is the shared observation boundary used by DatasetMetrics.
type DatasetRunObserver interface {
	ObserveRun(DatasetObservation) error
}

// DatasetModuleObserver records one authoritative terminal result in both the
// per-Dataset series and the low-cardinality module pipeline series. Dataset
// watermarks deliberately stay tuple-scoped: folding multiple Dataset/freq
// values into one module watermark would create false regressions.
type DatasetModuleObserver struct {
	datasets DatasetRunObserver
	module   *ModuleMetrics
	stage    string
	pipeline string
}

func NewDatasetModuleObserver(
	datasets DatasetRunObserver,
	module *ModuleMetrics,
	stage string,
	pipeline string,
) (*DatasetModuleObserver, error) {
	if datasets == nil || module == nil {
		return nil, fmt.Errorf("dataset and module metrics are required")
	}
	if !allowedStages[stage] {
		return nil, fmt.Errorf("unknown metrics stage %q", stage)
	}
	if !module.allowedPipelines[pipeline] {
		return nil, fmt.Errorf("unknown metrics pipeline %q", pipeline)
	}
	return &DatasetModuleObserver{datasets: datasets, module: module, stage: stage, pipeline: pipeline}, nil
}

func (o *DatasetModuleObserver) ObserveRun(observation DatasetObservation) error {
	if o == nil {
		return fmt.Errorf("dataset module observer is nil")
	}
	if err := o.datasets.ObserveRun(observation); err != nil {
		return err
	}
	result := "success"
	if observation.Result == "error" || observation.Result == "rejected" {
		result = observation.Result
	} else if observation.Result == "incomplete" {
		result = "error"
	}
	return o.module.ObserveRun(o.stage, result, o.pipeline, observation.FinishedAt)
}
