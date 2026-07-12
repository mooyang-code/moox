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
	return &State{
		Module:     module,
		InstanceID: instance,
		Version:    version,
		GitCommit:  commit,
		StartedAt:  time.Now().UTC(),
	}
}

func (s *State) Ready() bool { return s != nil && s.ReadyFlag.Load() }

func (s *State) SetReady(v bool) {
	if s != nil {
		s.ReadyFlag.Store(v)
	}
}

func (s *State) Snapshot(ctx context.Context) healthz.Response {
	if s != nil && s.SnapshotFunc != nil {
		rsp := s.SnapshotFunc(ctx)
		if !s.Ready() {
			rsp.Ready = false
			if rsp.Status == "ok" {
				rsp.Status = "degraded"
			}
		}
		return rsp
	}
	return healthz.Base(s.Module, s.InstanceID, s.Version, s.GitCommit, s.StartedAt, s.Ready())
}
