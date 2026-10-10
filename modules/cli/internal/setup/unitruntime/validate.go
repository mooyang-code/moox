package unitruntime

import "github.com/mooyang-code/moox/packages/servicecatalog"

// ValidateRelease checks all runtime inputs without starting processes or
// mutating shared state. Installers can use it on their private staging plan.
func ValidateRelease(planPath string) error {
	plan, err := LoadPlan(planPath)
	if err != nil {
		return err
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return err
	}
	state := runtimeState{plan: plan, catalog: catalog}
	for _, component := range plan.Components {
		if _, _, _, err := state.prepare(component); err != nil {
			return err
		}
	}
	return nil
}
