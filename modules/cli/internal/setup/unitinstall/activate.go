package unitinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
)

type ActivateOptions struct {
	Directory  string
	NoStart    bool
	Components []string
	StateSeed  *StateSeedReference
}

type Activation struct {
	Version           int       `json:"version"`
	HostID            string    `json:"host_id"`
	DeploymentRoot    string    `json:"deployment_root"`
	UnitRoot          string    `json:"unit_root"`
	Profile           string    `json:"profile"`
	Directory         string    `json:"directory"`
	PreviousDirectory string    `json:"previous_directory"`
	Phase             string    `json:"phase"`
	StartComponents   []string  `json:"start_components"`
	PreviousRunning   []string  `json:"previous_running"`
	StateSeedSHA256   string    `json:"state_seed_sha256,omitempty"`
	UpdatedAt         time.Time `json:"updated_at"`
}

func planPath(prepared Prepared) string { return filepath.Join(prepared.Directory, "runtime.json") }

// Activate stops old unit writers, copies their state, atomically changes
// current and starts selected new components under one host maintenance lock.
// Old data remains an independent stopped snapshot. Any failure after the
// journal is written restores that snapshot and its previous running selection.
func Activate(ctx context.Context, options ActivateOptions, lockOptions unitruntime.Options) (result Activation, returnErr error) {
	if !validStateReference(options.StateSeed) {
		return result, errors.New("activation state seed requires a producer SHA256 and directory")
	}
	candidate, err := readRelease(ctx, options.Directory, true, false)
	if err != nil {
		return result, err
	}
	starts := slices.Clone(options.Components)
	if options.NoStart && len(starts) != 0 {
		return result, errors.New("activation cannot combine no-start and a component selection")
	}
	if !options.NoStart && len(starts) == 0 {
		starts = slices.Clone(candidate.Components)
	}
	if !validSelection(starts, candidate.Components) {
		return result, errors.New("activation start selection must contain distinct unit components")
	}
	starts = lifecycleOrder(starts)
	err = unitruntime.WithMaintenance(ctx, candidate.DeploymentRoot, candidate.HostID, lockOptions, func(guard *unitruntime.Maintenance) error {
		unit, err := fsutil.OpenPhysicalRoot(candidate.UnitRoot, false)
		if err != nil {
			return err
		}
		defer unit.Close()
		last, exists, err := readActivation(unit)
		if err != nil {
			return err
		}
		if exists && (last.HostID != candidate.HostID || last.DeploymentRoot != candidate.DeploymentRoot || last.UnitRoot != candidate.UnitRoot || last.Profile != candidate.Profile) {
			return errors.New("unit activation journal belongs to a different host/root/profile")
		}
		if exists && last.Phase != "active" && last.Phase != "rolled-back" {
			if err := restore(ctx, guard, unit, &last); err != nil {
				result = last
				return err
			}
		}
		current, err := currentRelease(unit, candidate.UnitRoot)
		if err != nil {
			return err
		}
		if current == candidate.Directory {
			if !exists || last.Phase != "active" || last.Directory != current {
				return errors.New("current candidate has no completed activation journal")
			}
			if options.StateSeed != nil && options.StateSeed.SHA256 != last.StateSeedSHA256 {
				return errors.New("current release was activated with different state input")
			}
			if _, err := ReadInstalled(ctx, candidate.Directory); err != nil {
				return err
			}
			if err := guard.BeginInstallation(ctx, candidate.UnitRoot, candidate.Directory); err != nil {
				return err
			}
			result = last
			return guard.EndInstallation()
		}
		candidate, err = ReadPrepared(ctx, options.Directory)
		if err != nil {
			return err
		}
		var previous Prepared
		if current != "" {
			previous, err = ReadInstalled(ctx, current)
			if err != nil {
				return err
			}
			if err := sameUnit(candidate, previous); err != nil {
				return err
			}
		}
		seed, err := loadStateSeed(ctx, options.StateSeed, candidate, current)
		if err != nil {
			return err
		}
		proxy := slices.Contains(candidate.Components, "console-proxy")
		previousProxy := proxy && slices.Contains(previous.Components, "console-proxy")
		previousCA := ""
		if proxy {
			if err := validateProxyPaths(candidate); err != nil {
				return err
			}
		}
		if previousProxy {
			previousCA, err = proxyState(ctx, previous)
			if err != nil {
				return err
			}
		}
		allowed := []string{candidate.Directory}
		if current != "" {
			allowed = append(allowed, current)
		}
		for _, release := range []Prepared{candidate, previous} {
			if release.Directory != "" {
				if err := guard.CheckUnit(ctx, planPath(release), allowed); err != nil {
					return err
				}
			}
		}
		journal := Activation{Version: 1, HostID: candidate.HostID, DeploymentRoot: candidate.DeploymentRoot, UnitRoot: candidate.UnitRoot, Profile: candidate.Profile, Directory: candidate.Directory, PreviousDirectory: current, Phase: "stopping", StartComponents: starts, PreviousRunning: []string{}}
		if options.StateSeed != nil {
			journal.StateSeedSHA256 = options.StateSeed.SHA256
		}
		if current != "" {
			status, err := guard.Execute(ctx, planPath(previous), "status", lifecycleOrder(previous.Components))
			if err != nil {
				return err
			}
			for _, component := range status.Components {
				if component.State == "running" {
					journal.PreviousRunning = append(journal.PreviousRunning, component.ID)
				}
			}
		}
		if err := writeActivation(unit, &journal, "stopping"); err != nil {
			return err
		}
		if err := guard.BeginInstallation(ctx, candidate.UnitRoot, candidate.Directory); err != nil {
			return err
		}
		fail := func(cause error) error {
			// Once old services have been touched, cancellation cannot release
			// maintenance while a partial new unit remains running. Individual
			// lifecycle calls still use their persisted, bounded stop/start budgets.
			recoveryErr := restore(context.WithoutCancel(ctx), guard, unit, &journal)
			result = journal
			return errors.Join(cause, recoveryErr)
		}
		if current != "" {
			if _, err := guard.Execute(ctx, planPath(previous), "stop", lifecycleOrder(previous.Components)); err != nil {
				return fail(err)
			}
		}
		if previousProxy {
			stoppedCA, err := proxyState(ctx, previous)
			if err != nil || stoppedCA != previousCA {
				return fail(errors.New("stopped proxy CA does not match its pre-stop identity"))
			}
		}
		if err := writeActivation(unit, &journal, "copying"); err != nil {
			return fail(err)
		}
		if current != "" {
			if err := copyState(ctx, previous, candidate, seed.Paths); err != nil {
				return fail(err)
			}
		}
		if err := importState(ctx, seed, candidate); err != nil {
			return fail(err)
		}
		if _, err := ReadInstalled(ctx, candidate.Directory); err != nil {
			return fail(err)
		}
		if proxy {
			copiedCA, err := proxyState(ctx, candidate)
			if err != nil || (previousProxy && copiedCA != previousCA) {
				return fail(errors.New("copied/imported proxy CA identity failed validation"))
			}
		}
		if err := ctx.Err(); err != nil {
			return fail(err)
		}
		if err := writeActivation(unit, &journal, "switching"); err != nil {
			return fail(err)
		}
		if err := setCurrent(unit, candidate.Directory); err != nil {
			return fail(err)
		}
		if err := writeActivation(unit, &journal, "starting"); err != nil {
			return fail(err)
		}
		if len(starts) != 0 {
			started, err := guard.Execute(ctx, planPath(candidate), "start", starts)
			if err != nil {
				return fail(err)
			}
			for _, component := range started.Components {
				if component.State != "paused" && !component.Ready {
					return fail(errors.New("activated component did not become ready"))
				}
				if component.ID == "console-proxy" && component.Ready {
					activeCA, err := proxyState(ctx, candidate)
					if err != nil || (previousProxy && activeCA != previousCA) {
						return fail(errors.New("activated proxy CA identity failed validation"))
					}
				}
			}
		}
		if err := writeActivation(unit, &journal, "active"); err != nil {
			return fail(err)
		}
		result = journal
		return guard.EndInstallation()
	})
	return result, err
}

func validSelection(ids, available []string) bool {
	seen := map[string]bool{}
	for _, id := range ids {
		if seen[id] || !slices.Contains(available, id) {
			return false
		}
		seen[id] = true
	}
	return true
}

func sameUnit(a, b Prepared) error {
	if a.HostID != b.HostID || a.DeploymentRoot != b.DeploymentRoot || a.UnitRoot != b.UnitRoot || a.HostUnitRoot != b.HostUnitRoot || a.Profile != b.Profile {
		return errors.New("activation releases must belong to the same host/root/profile")
	}
	return nil
}

func currentRelease(unit *os.Root, unitRoot string) (string, error) {
	info, err := unit.Lstat("current")
	if os.IsNotExist(err) {
		return "", nil
	}
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", errors.New("unit current must be a release directory link")
	}
	physical, err := filepath.EvalSymlinks(filepath.Join(unitRoot, "current"))
	if err != nil || filepath.Dir(physical) != filepath.Join(unitRoot, "releases") || !validReleaseID(filepath.Base(physical)) {
		return "", errors.New("unit current must select a bounded physical release")
	}
	return physical, nil
}
