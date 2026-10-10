package unitruntime

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
)

type bootstrap struct {
	HostID        string `json:"host_id"`
	RequestSHA256 string `json:"request_sha256"`
	Token         string `json:"token"`
}

func bootstrapDigest(value string) bool {
	return strings.HasPrefix(value, "sha256:") && validDigest(strings.TrimPrefix(value, "sha256:"))
}

func (s *runtimeState) pendingBootstrap() (*bootstrap, error) {
	var pending bootstrap
	err := privateJSON(filepath.Join(s.run.Name(), "bootstrap.json"), &pending)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if pending.HostID != s.plan.HostID || !bootstrapDigest(pending.RequestSHA256) || len(pending.Token) < 16 {
		return nil, errors.New("persistent bootstrap barrier has an invalid host or request identity")
	}
	return &pending, nil
}

// BeginBootstrap protects the entire host/control sequence across unit
// activations and process death. Its identity is the coordinator's canonical
// request digest, so another workflow cannot silently adopt interrupted work.
func (m *Maintenance) BeginBootstrap(ctx context.Context, requestSHA256 string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock == nil {
		return errors.New("maintenance callback has ended")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !bootstrapDigest(requestSHA256) {
		return errors.New("bootstrap requires a canonical request SHA256")
	}
	state, err := openRuntime(Plan{HostID: m.hostID, DeploymentRoot: m.root})
	if err != nil {
		return err
	}
	defer state.close()
	pending, err := state.pendingBootstrap()
	if err != nil {
		return err
	}
	if pending != nil {
		if pending.RequestSHA256 != requestSHA256 {
			return errors.New("another interrupted bootstrap requires recovery")
		}
		m.bootstrap = pending
		return nil
	}
	pending = &bootstrap{HostID: m.hostID, RequestSHA256: requestSHA256, Token: rand.Text()}
	if err := writeState(state.run, "bootstrap.json", pending); err != nil {
		return err
	}
	m.bootstrap = pending
	return nil
}

func (m *Maintenance) EndBootstrap() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.lock == nil || m.bootstrap == nil {
		return errors.New("maintenance has no bootstrap barrier to complete")
	}
	state, err := openRuntime(Plan{HostID: m.hostID, DeploymentRoot: m.root})
	if err != nil {
		return err
	}
	defer state.close()
	pending, err := state.pendingBootstrap()
	if err != nil {
		return err
	}
	if pending == nil || *pending != *m.bootstrap {
		return errors.New("bootstrap barrier changed before completion")
	}
	if installation, err := state.pendingInstallation(); err != nil {
		return err
	} else if installation != nil {
		return errors.New("bootstrap cannot complete with an unfinished unit installation")
	}
	if err := state.run.Remove("bootstrap.json"); err != nil {
		return err
	}
	m.bootstrap = nil
	directory, err := state.run.Open(".")
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
