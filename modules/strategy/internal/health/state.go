package health

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/packages/healthz"
)

type State struct {
	Module       string
	InstanceID   string
	Version      string
	GitCommit    string
	StartedAt    time.Time
	ReadyFlag    atomic.Bool
	SnapshotFunc func(context.Context) healthz.Response
}

func New(module, instance, version, commit string) *State {
	return &State{Module: module, InstanceID: instance, Version: version, GitCommit: commit, StartedAt: time.Now().UTC()}
}

func (s *State) SetReady(value bool) {
	if s != nil {
		s.ReadyFlag.Store(value)
	}
}

func (s *State) Ready() bool { return s != nil && s.ReadyFlag.Load() }

func (s *State) Snapshot(ctx context.Context) healthz.Response {
	if s != nil && s.SnapshotFunc != nil {
		return s.SnapshotFunc(ctx)
	}
	return healthz.Base(s.Module, s.InstanceID, s.Version, s.GitCommit, s.StartedAt, s.Ready())
}
