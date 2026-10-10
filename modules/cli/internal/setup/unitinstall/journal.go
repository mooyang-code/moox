package unitinstall

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitruntime"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

func readActivation(unit *os.Root) (Activation, bool, error) {
	if _, err := unit.Lstat("activation.json"); os.IsNotExist(err) {
		return Activation{}, false, nil
	} else if err != nil {
		return Activation{}, false, err
	}
	raw, err := fsutil.ReadPrivate(unit, "activation.json", 128<<10)
	if err != nil {
		return Activation{}, false, err
	}
	var journal Activation
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&journal) != nil || decoder.Decode(new(any)) != io.EOF {
		return Activation{}, false, errors.New("activation journal requires one known JSON document")
	}
	canonical, err := json.Marshal(journal)
	if err != nil || !bytes.Equal(raw, append(canonical, '\n')) {
		return Activation{}, false, errors.New("activation journal must use canonical JSON")
	}
	if journal.Version != 1 || !servicecatalog.ValidHostID(journal.HostID) || journal.UnitRoot != unit.Name() || journal.UpdatedAt.IsZero() || !slices.Contains([]string{"stopping", "copying", "switching", "starting", "active", "restoring", "rolled-back"}, journal.Phase) {
		return Activation{}, false, errors.New("activation journal has an invalid identity or phase")
	}
	for _, root := range []string{journal.Directory, journal.PreviousDirectory} {
		if root != "" && (filepath.Dir(root) != filepath.Join(journal.UnitRoot, "releases") || !validReleaseID(filepath.Base(root))) {
			return Activation{}, false, errors.New("activation journal release escapes its unit")
		}
	}
	if journal.Directory == "" || journal.Directory == journal.PreviousDirectory || len(journal.StartComponents) > 64 || len(journal.PreviousRunning) > 64 {
		return Activation{}, false, errors.New("activation journal requires bounded distinct release identities")
	}
	if journal.StateSeedSHA256 != "" && !validStateReference(&StateSeedReference{Directory: "journal", SHA256: journal.StateSeedSHA256}) {
		return Activation{}, false, errors.New("activation journal state seed digest is invalid")
	}
	return journal, true, nil
}

func writeActivation(unit *os.Root, journal *Activation, phase string) error {
	journal.Phase, journal.UpdatedAt = phase, time.Now().UTC()
	raw, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return fsutil.WritePrivate(unit, "activation.json", append(raw, '\n'), true)
}

func setCurrent(unit *os.Root, directory string) error {
	if directory == "" {
		if err := unit.Remove("current"); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else {
		temporary := ".current-" + rand.Text()
		if err := unit.Symlink(directory, temporary); err != nil {
			return err
		}
		defer unit.Remove(temporary)
		if err := unit.Rename(temporary, "current"); err != nil {
			return err
		}
	}
	directoryFile, err := unit.Open(".")
	if err != nil {
		return err
	}
	defer directoryFile.Close()
	return directoryFile.Sync()
}

// restore also handles a process death at any journal phase. It stops both
// recorded release selections before changing current. The previous snapshot
// remains unchanged; explicit rollback restores that pre-upgrade data snapshot.
func restore(ctx context.Context, guard *unitruntime.Maintenance, unit *os.Root, journal *Activation) error {
	candidate, err := ReadInstalled(ctx, journal.Directory)
	if err != nil {
		return err
	}
	if candidate.HostID != journal.HostID || candidate.DeploymentRoot != journal.DeploymentRoot || candidate.UnitRoot != journal.UnitRoot || candidate.Profile != journal.Profile || !validSelection(journal.StartComponents, candidate.Components) {
		return errors.New("activation recovery candidate does not match its journal")
	}
	var previous Prepared
	if journal.PreviousDirectory != "" {
		previous, err = ReadInstalled(ctx, journal.PreviousDirectory)
		if err != nil {
			return err
		}
		if err := sameUnit(candidate, previous); err != nil {
			return err
		}
	}
	if !validSelection(journal.PreviousRunning, previous.Components) {
		return errors.New("activation recovery running selection does not match the previous unit")
	}
	current, err := currentRelease(unit, journal.UnitRoot)
	if err != nil {
		return err
	}
	if current != journal.Directory && current != journal.PreviousDirectory {
		return errors.New("activation recovery refuses an unrelated current release")
	}
	allowed := []string{journal.Directory, journal.PreviousDirectory}
	for _, release := range []Prepared{candidate, previous} {
		if release.Directory != "" {
			if err := guard.CheckUnit(ctx, planPath(release), allowed); err != nil {
				return err
			}
		}
	}
	if err := guard.BeginInstallation(ctx, journal.UnitRoot, journal.Directory); err != nil {
		return err
	}
	phaseErr := writeActivation(unit, journal, "restoring")
	for _, release := range []Prepared{candidate, previous} {
		if release.Directory != "" {
			if _, err := guard.Execute(ctx, planPath(release), "stop", lifecycleOrder(release.Components)); err != nil {
				return err
			}
		}
	}
	if err := setCurrent(unit, journal.PreviousDirectory); err != nil {
		return err
	}
	if len(journal.PreviousRunning) != 0 {
		started, err := guard.Execute(ctx, planPath(previous), "start", lifecycleOrder(journal.PreviousRunning))
		if err != nil {
			return err
		}
		for _, component := range started.Components {
			if component.State != "paused" && !component.Ready {
				return errors.New("previous component did not become ready during recovery")
			}
		}
	}
	if err := writeActivation(unit, journal, "rolled-back"); err != nil {
		return errors.Join(phaseErr, err)
	}
	if phaseErr != nil {
		return phaseErr
	}
	return guard.EndInstallation()
}

// Recover restores a pending transaction, or clears an already completed
// transaction's leftover barrier. It does not roll back a successful release.
func Recover(ctx context.Context, unitRoot string, lockOptions unitruntime.Options) (Activation, error) {
	return runRecovery(ctx, unitRoot, false, lockOptions)
}

// Rollback explicitly restores the last activation's pre-upgrade snapshot and
// its previous running selection. Changes made after that upgrade stay in the
// retired new release, rather than being merged into an older schema.
func Rollback(ctx context.Context, unitRoot string, lockOptions unitruntime.Options) (Activation, error) {
	return runRecovery(ctx, unitRoot, true, lockOptions)
}

func runRecovery(ctx context.Context, unitRoot string, rollback bool, lockOptions unitruntime.Options) (result Activation, returnErr error) {
	unit, err := fsutil.OpenPhysicalRoot(unitRoot, false)
	if err != nil {
		return result, err
	}
	defer unit.Close()
	journal, exists, err := readActivation(unit)
	if err != nil || !exists {
		return result, errors.Join(err, errors.New("unit has no valid activation journal"))
	}
	err = unitruntime.WithMaintenance(ctx, journal.DeploymentRoot, journal.HostID, lockOptions, func(guard *unitruntime.Maintenance) error {
		latest, exists, err := readActivation(unit)
		if err != nil || !exists || latest.HostID != journal.HostID || latest.DeploymentRoot != journal.DeploymentRoot {
			return errors.Join(err, errors.New("activation journal changed while acquiring maintenance"))
		}
		journal = latest
		if rollback && journal.Phase == "active" && journal.PreviousDirectory == "" {
			return errors.New("first installation has no previous snapshot to roll back")
		}
		if (journal.Phase == "active" && !rollback) || journal.Phase == "rolled-back" {
			wanted := journal.Directory
			if journal.Phase == "rolled-back" {
				wanted = journal.PreviousDirectory
			}
			current, err := currentRelease(unit, journal.UnitRoot)
			if err != nil || current != wanted {
				return errors.Join(err, errors.New("completed activation no longer matches current"))
			}
			if wanted != "" {
				if _, err := ReadInstalled(ctx, wanted); err != nil {
					return err
				}
			}
			if err := guard.BeginInstallation(ctx, journal.UnitRoot, journal.Directory); err != nil {
				return err
			}
			result = journal
			return guard.EndInstallation()
		}
		err = restore(ctx, guard, unit, &journal)
		result = journal
		return err
	})
	return result, err
}
