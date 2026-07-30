package monitorpb

import "fmt"

const (
	MaxDoctorContextComponents   = 64
	MaxDoctorContextHealthChecks = 32
	MaxDoctorContextBytes        = 2 << 20
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
	if len(r.HealthCheckIds) > MaxDoctorContextHealthChecks {
		return fmt.Errorf("health_check_ids exceeds limit %d", MaxDoctorContextHealthChecks)
	}
	if err := validateUniqueNonEmpty("component_ids", r.ComponentIds); err != nil {
		return err
	}
	if err := validateUniqueNonEmpty("health_check_ids", r.HealthCheckIds); err != nil {
		return err
	}
	return nil
}

func validateUniqueNonEmpty(name string, values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" {
			return fmt.Errorf("%s contains an empty value", name)
		}
		if _, ok := seen[value]; ok {
			return fmt.Errorf("%s contains duplicate %q", name, value)
		}
		seen[value] = struct{}{}
	}
	return nil
}
