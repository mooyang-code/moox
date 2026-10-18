package unitruntime

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
)

type installation struct {
	HostID      string `json:"host_id"`
	UnitRoot    string `json:"unit_root"`
	ReleaseRoot string `json:"release_root"`
	Token       string `json:"token"`
}

func (s *runtimeState) pendingInstallation() (*installation, error) {
	var pending installation
	err := privateJSON(filepath.Join(s.run.Name(), "installation.json"), &pending)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pending.HostID != s.plan.HostID || len(pending.Token) < 16 || !filepath.IsAbs(pending.UnitRoot) || filepath.Clean(pending.UnitRoot) != pending.UnitRoot || filepath.Dir(filepath.Dir(pending.ReleaseRoot)) != pending.UnitRoot || filepath.Base(filepath.Dir(pending.ReleaseRoot)) != "releases" {
		return nil, errors.New("persistent installation barrier has an invalid host or unit identity")
	}
	return &pending, nil
}

// BeginInstallation keeps automatic starts blocked even if the installer dies
// and the kernel releases flock. Only recovery for this same unit/release may
// adopt the barrier. Pause markers remain independent.
func (m *Maintenance) BeginInstallation(ctx context.Context, unitRoot, releaseRoot string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock == nil {
		return errors.New("maintenance callback has ended")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := physicalDirectory(unitRoot); err != nil {
		return err
	}
	if err := physicalDirectory(releaseRoot); err != nil {
		return err
	}
	if filepath.Dir(releaseRoot) != filepath.Join(unitRoot, "releases") {
		return errors.New("installation barrier requires a release inside its unit")
	}
	state, err := openRuntime(Plan{HostID: m.hostID, DeploymentRoot: m.root})
	if err != nil {
		return err
	}
	defer state.close()
	pending, err := state.pendingInstallation()
	if err != nil {
		return err
	}
	if pending != nil {
		if pending.UnitRoot != unitRoot || pending.ReleaseRoot != releaseRoot {
			return errors.New("another interrupted installation requires recovery")
		}
		m.installation = pending
		return nil
	}
	pending = &installation{HostID: m.hostID, UnitRoot: unitRoot, ReleaseRoot: releaseRoot, Token: rand.Text()}
	if err := writeState(state.run, "installation.json", pending); err != nil {
		return err
	}
	m.installation = pending
	return nil
}

func (m *Maintenance) EndInstallation() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock == nil || m.installation == nil {
		return errors.New("maintenance has no installation barrier to complete")
	}
	state, err := openRuntime(Plan{HostID: m.hostID, DeploymentRoot: m.root})
	if err != nil {
		return err
	}
	defer state.close()
	pending, err := state.pendingInstallation()
	if err != nil {
		return err
	}
	if pending == nil || *pending != *m.installation {
		return errors.New("installation barrier changed before completion")
	}
	if err := state.run.Remove("installation.json"); err != nil {
		return err
	}
	m.installation = nil
	directory, err := state.run.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
