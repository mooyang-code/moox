package unitruntime

// ValidateRelease checks all runtime inputs of an installed release without
// starting processes or mutating shared state; its components are defined by
// the service catalog the release shipped with.
func ValidateRelease(planPath string) error {
	return validate(planPath, false)
}

// ValidateCandidate checks a prepared or staged release the same way, and also
// requires it to use this runtime's embedded service catalog. Installers use it
// on their private staging plan.
func ValidateCandidate(planPath string) error {
	return validate(planPath, true)
}

func validate(planPath string, candidate bool) error {
	plan, catalog, err := loadPlan(planPath, candidate)
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
