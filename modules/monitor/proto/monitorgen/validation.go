package monitorpb

import "fmt"

const (
	MaxDoctorContextComponents = 64
	MaxDoctorContextPipelines  = 32
	MaxDoctorContextBytes      = 2 << 20
)

func (r *CreateCheckReq) Validate() error {
	if r == nil || r.Check == nil {
		return fmt.Errorf("check is required")
	}
	return nil
}

func (r *GetDoctorContextReq) Validate() error {
	if r == nil {
		return fmt.Errorf("request is required")
	}
	if len(r.ComponentIds) > MaxDoctorContextComponents {
		return fmt.Errorf("component_ids exceeds limit %d", MaxDoctorContextComponents)
	}
	if len(r.PipelineIds) > MaxDoctorContextPipelines {
		return fmt.Errorf("pipeline_ids exceeds limit %d", MaxDoctorContextPipelines)
	}
	return nil
}
