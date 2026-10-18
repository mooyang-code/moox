package unitbootstrap

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitinstall"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
)

func checkUnit(ctx context.Context, prepared unitinstall.Prepared, allowed []string, lock unitruntime.Options) error {
	return unitruntime.WithMaintenance(ctx, prepared.DeploymentRoot, prepared.HostID, lock, func(g *unitruntime.Maintenance) error {
		return g.CheckUnit(ctx, filepath.Join(prepared.Directory, "runtime.json"), allowed)
	})
}

func lifecycle(ctx context.Context, directory, operation string, ids []string, lock unitruntime.Options) (unitruntime.Result, error) {
	return unitruntime.Execute(ctx, filepath.Join(directory, "runtime.json"), operation, ids, lock)
}

func start(ctx context.Context, directory string, ids []string, required bool, lock unitruntime.Options) error {
	if len(ids) == 0 {
		return nil
	}
	result, err := lifecycle(ctx, directory, "start", ids, lock)
	if err != nil {
		return err
	}
	for _, component := range result.Components {
		if component.State == "paused" && !required {
			continue
		}
		if component.State != "running" || !component.Ready {
			return errors.New("bootstrap component did not become ready; required dependencies cannot remain paused")
		}
	}
	return nil
}

func remaining(components []string, omit ...string) []string {
	return slices.DeleteFunc(slices.Clone(components), func(id string) bool { return slices.Contains(omit, id) })
}

func restoreWorkflow(ctx context.Context, root *os.Root, j *journal, lock unitruntime.Options) error {
	if err := writeJournal(root, j, "recovering"); err != nil {
		return err
	}
	units := []unitState{j.Control, j.Host}
	// Validate every recorded release and PID owner before stopping either unit.
	for i, u := range units {
		profile := "control"
		if i == 1 {
			profile = "host"
		}
		active, err := current(ctx, u.Root, j.DeploymentRoot, j.HostID, profile)
		if err != nil {
			return err
		}
		if active.Directory != u.Previous && active.Directory != u.Candidate {
			return errors.New("bootstrap recovery refuses an unrelated current release")
		}
		for _, directory := range []string{u.Candidate, u.Previous} {
			if directory == "" {
				continue
			}
			prepared, err := unitinstall.ReadInstalled(ctx, directory)
			if err != nil {
				return err
			}
			if prepared.UnitRoot != u.Root || prepared.DeploymentRoot != j.DeploymentRoot || prepared.HostID != j.HostID || prepared.Profile != profile {
				return errors.New("bootstrap recovery release does not match its journal")
			}
			if err := checkUnit(ctx, prepared, []string{u.Candidate, u.Previous}, lock); err != nil {
				return err
			}
		}
		if u.Previous == "" && len(u.Running) != 0 {
			return errors.New("bootstrap journal cannot restart components without a previous release")
		}
		if u.Previous != "" {
			previous, err := unitinstall.ReadInstalled(ctx, u.Previous)
			if err != nil {
				return err
			}
			seen := map[string]bool{}
			for _, id := range u.Running {
				if seen[id] || !slices.Contains(previous.Components, id) {
					return errors.New("bootstrap recovery running selection is invalid")
				}
				seen[id] = true
			}
		}
	}
	for _, u := range units {
		for _, directory := range []string{u.Candidate, u.Previous} {
			if directory != "" {
				if _, err := lifecycle(ctx, directory, "stop", nil, lock); err != nil {
					return err
				}
			}
		}
	}
	// An interrupted unit owns the single installation barrier. Clear that unit
	// first, before asking completed units to adopt their own transaction locks.
	for _, u := range units {
		last, exists, err := unitinstall.ReadActivation(u.Root)
		if err != nil {
			return err
		}
		if exists && last.Directory == u.Candidate && last.Phase != "active" && last.Phase != "rolled-back" {
			if _, err := unitinstall.Recover(ctx, u.Root, lock); err != nil {
				return err
			}
		}
	}
	for _, u := range units {
		last, exists, err := unitinstall.ReadActivation(u.Root)
		if err != nil {
			return err
		}
		if exists && u.Candidate != "" && last.Directory == u.Candidate {
			if last.PreviousDirectory != u.Previous {
				return errors.New("unit activation does not match the bootstrap rollback snapshot")
			}
			if _, err := unitinstall.Abort(ctx, u.Candidate, lock); err != nil {
				return err
			}
		}
	}
	if j.Control.Previous != "" {
		if slices.Contains(j.Control.Running, "admin") {
			if err := start(ctx, j.Control.Previous, []string{"admin"}, true, lock); err != nil {
				return err
			}
		}
		if slices.Contains(j.Host.Running, "host-gateway") {
			if err := start(ctx, j.Host.Previous, []string{"host-gateway"}, true, lock); err != nil {
				return err
			}
		}
		if slices.Contains(j.Control.Running, "eventbus") {
			if err := start(ctx, j.Control.Previous, []string{"eventbus"}, true, lock); err != nil {
				return err
			}
		}
		if err := start(ctx, j.Host.Previous, remaining(j.Host.Running, "host-gateway"), false, lock); err != nil {
			return err
		}
		if err := start(ctx, j.Control.Previous, remaining(j.Control.Running, "admin", "eventbus"), false, lock); err != nil {
			return err
		}
	}
	return writeJournal(root, j, "rolled-back")
}

var errNotReady = errors.New("completed bootstrap contains a component that is not ready")

func verifyComplete(ctx context.Context, j journal, lock unitruntime.Options) error {
	ready := true
	for i, u := range []unitState{j.Host, j.Control} {
		profile := "host"
		if i == 1 {
			profile = "control"
		}
		active, err := current(ctx, u.Root, j.DeploymentRoot, j.HostID, profile)
		if err != nil {
			return err
		}
		if active.Directory == "" || active.Directory != u.Candidate {
			return errors.New("completed bootstrap no longer matches current units")
		}
		if err := checkUnit(ctx, active, []string{u.Candidate}, lock); err != nil {
			return err
		}
		result, err := lifecycle(ctx, u.Candidate, "status", nil, lock)
		if err != nil {
			return err
		}
		for _, component := range result.Components {
			if component.ReadinessError != "" {
				return errors.New("bootstrap readiness probe failed for " + component.ID)
			}
			if component.State == "paused" && component.ID != "admin" && component.ID != "host-gateway" && component.ID != "eventbus" {
				continue
			}
			if component.State != "running" || !component.Ready {
				ready = false
			}
		}
	}
	if !ready {
		return errNotReady
	}
	return nil
}

func repairComplete(ctx context.Context, root *os.Root, j *journal, lock unitruntime.Options) error {
	err := verifyComplete(ctx, *j, lock)
	if err == nil {
		if j.Phase == "repairing" {
			return writeJournal(root, j, "complete")
		}
		return nil
	}
	if !errors.Is(err, errNotReady) {
		return err
	}
	if err := writeJournal(root, j, "repairing"); err != nil {
		return err
	}
	for _, directory := range []string{j.Control.Candidate, j.Host.Candidate} {
		if _, err := lifecycle(ctx, directory, "stop", nil, lock); err != nil {
			return err
		}
	}
	control, err := unitinstall.ReadInstalled(ctx, j.Control.Candidate)
	if err != nil {
		return err
	}
	if err := startStages(ctx, *j, control.Components, lock); err != nil {
		return err
	}
	if err := verifyComplete(ctx, *j, lock); err != nil {
		return err
	}
	return writeJournal(root, j, "complete")
}

func startStages(ctx context.Context, j journal, controlComponents []string, lock unitruntime.Options) error {
	if err := start(ctx, j.Control.Candidate, []string{"admin"}, true, lock); err != nil {
		return err
	}
	if err := start(ctx, j.Host.Candidate, []string{"host-gateway"}, true, lock); err != nil {
		return err
	}
	if err := start(ctx, j.Control.Candidate, []string{"eventbus"}, true, lock); err != nil {
		return err
	}
	if err := start(ctx, j.Host.Candidate, []string{"host-agent"}, false, lock); err != nil {
		return err
	}
	return start(ctx, j.Control.Candidate, remaining(controlComponents, "admin", "eventbus"), false, lock)
}
