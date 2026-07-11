package health

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/packages/healthz"
)

type State struct {
	Module           string
	InstanceID       string
	Version          string
	GitCommit        string
	StartedAt        time.Time
	ReadyFlag        atomic.Bool
	NATSReady        atomic.Bool
	JournalReady     atomic.Bool
	DirtyPartitions  atomic.Uint64
	PendingRows      atomic.Uint64
	COSPending       atomic.Uint64
	CosEnabled       bool
	LastMaterialized atomic.Int64
}

func New(module, instance, version, commit string) *State {
	return &State{Module: module, InstanceID: instance, Version: version, GitCommit: commit, StartedAt: time.Now().UTC()}
}
func (s *State) Ready() bool { return s != nil && s.ReadyFlag.Load() }
func (s *State) Snapshot(context.Context) healthz.Response {
	rsp := healthz.Base(s.Module, s.InstanceID, s.Version, s.GitCommit, s.StartedAt, s.Ready())
	last := s.LastMaterialized.Load()
	details := map[string]any{"nats_ready": s.NATSReady.Load(), "journal_ready": s.JournalReady.Load(), "dirty_partitions": s.DirtyPartitions.Load(), "pending_rows": s.PendingRows.Load(), "cos_enabled": s.CosEnabled, "cos_pending_files": s.COSPending.Load()}
	if last > 0 {
		details["last_materialized_at"] = time.Unix(0, last).UTC().Format(time.RFC3339Nano)
	}
	rsp.Details = details
	return rsp
}
