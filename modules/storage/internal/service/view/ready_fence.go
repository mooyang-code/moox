package view

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

var ErrViewDataReadyPending = errors.New("view data ready waiting for applied positions")

type appliedFenceKey struct {
	spaceID string
	viewID  string
	indexID string
	nodeID  string
	storeID string
}

type persistedPendingReady struct {
	EventID          string              `json:"event_id"`
	SpaceID          string              `json:"space_id"`
	ViewID           string              `json:"view_id"`
	OccurredUnixNano int64               `json:"occurred_unix_nano"`
	Required         []persistedPosition `json:"required"`
	Payload          []byte              `json:"payload"`
}

type persistedPosition struct {
	NodeID   string `json:"node_id"`
	StoreID  string `json:"store_id"`
	Sequence uint64 `json:"sequence"`
}

type persistedApplied struct {
	SpaceID  string `json:"space_id"`
	ViewID   string `json:"view_id"`
	IndexID  string `json:"index_id"`
	NodeID   string `json:"node_id"`
	StoreID  string `json:"store_id"`
	Sequence uint64 `json:"sequence"`
}

func (s *Service) OpenReadyFence(dir string) error {
	if s == nil {
		return errors.New("view service is nil")
	}
	s.readyFenceDir = strings.TrimSpace(dir)
	if s.appliedFence == nil {
		s.appliedFence = make(map[appliedFenceKey]uint64)
	}
	return s.loadReadyFence()
}

func (s *Service) loadReadyFence() error {
	if s == nil || strings.TrimSpace(s.readyFenceDir) == "" {
		return nil
	}
	if err := os.MkdirAll(s.readyFenceDir, 0o755); err != nil {
		return err
	}
	if err := s.loadAppliedFence(); err != nil {
		return err
	}
	return s.loadPendingReady()
}

func (s *Service) persistAppliedFence() error {
	if s == nil || strings.TrimSpace(s.readyFenceDir) == "" {
		return nil
	}
	s.appliedFenceMu.Lock()
	items := make([]persistedApplied, 0, len(s.appliedFence))
	for key, seq := range s.appliedFence {
		items = append(items, persistedApplied{
			SpaceID: key.spaceID, ViewID: key.viewID, IndexID: key.indexID,
			NodeID: key.nodeID, StoreID: key.storeID, Sequence: seq,
		})
	}
	s.appliedFenceMu.Unlock()
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(s.readyFenceDir, "applied.json"), raw)
}

func (s *Service) loadAppliedFence() error {
	raw, err := os.ReadFile(filepath.Join(s.readyFenceDir, "applied.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var items []persistedApplied
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	s.appliedFenceMu.Lock()
	defer s.appliedFenceMu.Unlock()
	if s.appliedFence == nil {
		s.appliedFence = make(map[appliedFenceKey]uint64)
	}
	for _, item := range items {
		key := appliedFenceKey{spaceID: item.SpaceID, viewID: item.ViewID, indexID: item.IndexID, nodeID: item.NodeID, storeID: item.StoreID}
		if item.Sequence > s.appliedFence[key] {
			s.appliedFence[key] = item.Sequence
		}
	}
	return nil
}

func (s *Service) persistPendingReadyLocked() error {
	if s == nil || strings.TrimSpace(s.readyFenceDir) == "" {
		return nil
	}
	items := make([]persistedPendingReady, 0, len(s.pendingReady))
	for _, item := range s.pendingReady {
		payload, err := proto.Marshal(item.payload)
		if err != nil {
			return err
		}
		required := make([]persistedPosition, 0, len(item.required))
		for _, pos := range item.required {
			if pos == nil {
				continue
			}
			required = append(required, persistedPosition{NodeID: pos.GetNodeId(), StoreID: pos.GetStoreId(), Sequence: pos.GetSequence()})
		}
		items = append(items, persistedPendingReady{
			EventID: item.opts.EventID, SpaceID: item.spaceID, ViewID: item.viewID,
			OccurredUnixNano: item.opts.OccurredAt.UnixNano(), Required: required, Payload: payload,
		})
	}
	raw, err := json.Marshal(items)
	if err != nil {
		return err
	}
	return atomicWriteFile(filepath.Join(s.readyFenceDir, "pending.json"), raw)
}

func (s *Service) loadPendingReady() error {
	raw, err := os.ReadFile(filepath.Join(s.readyFenceDir, "pending.json"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var items []persistedPendingReady
	if err := json.Unmarshal(raw, &items); err != nil {
		return err
	}
	pending := make([]pendingViewReady, 0, len(items))
	for _, item := range items {
		payload := &storagepb.ViewDataReady{}
		if err := proto.Unmarshal(item.Payload, payload); err != nil {
			return err
		}
		required := make([]*storagepb.CommittedPosition, 0, len(item.Required))
		for _, pos := range item.Required {
			required = append(required, &storagepb.CommittedPosition{NodeId: pos.NodeID, StoreId: pos.StoreID, Sequence: pos.Sequence})
		}
		pending = append(pending, pendingViewReady{
			spaceID: item.SpaceID, viewID: item.ViewID, required: required, payload: payload,
			opts: events.PublishOptions{
				EventID: item.EventID, OccurredAt: time.Unix(0, item.OccurredUnixNano).UTC(),
				SpaceID: item.SpaceID, SubjectID: item.ViewID,
			},
		})
	}
	s.pendingReadyMu.Lock()
	s.pendingReady = pending
	s.pendingReadyMu.Unlock()
	return nil
}

func (s *Service) pendingReadyContains(eventID string) bool {
	if s == nil || strings.TrimSpace(eventID) == "" {
		return false
	}
	s.pendingReadyMu.Lock()
	defer s.pendingReadyMu.Unlock()
	for _, item := range s.pendingReady {
		if item.opts.EventID == eventID {
			return true
		}
	}
	return false
}

func (s *Service) appliedSequence(spaceID, viewID, indexID, nodeID, storeID string) uint64 {
	if s == nil {
		return 0
	}
	key := appliedFenceKey{spaceID: spaceID, viewID: viewID, indexID: indexID, nodeID: nodeID, storeID: storeID}
	s.appliedFenceMu.Lock()
	seq := s.appliedFence[key]
	s.appliedFenceMu.Unlock()
	return seq
}

func atomicWriteFile(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
