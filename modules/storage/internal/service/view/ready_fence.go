package view

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/events"
	storagepb "github.com/mooyang-code/moox/packages/storagepb"
	"google.golang.org/protobuf/proto"
)

var ErrViewDataReadyPending = errors.New("View 就绪事件等待对应的行写入")

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

// persistAppliedFence 落盘写入围栏。多个分区消费者并行写行，落盘必须串行：快照在持有 appliedPersistMu 时取，
// 后写入的文件总是更新的快照。已退役索引（View 当前活动索引之外）的键不再参与判断，顺带清掉。
func (s *Service) persistAppliedFence() error {
	if s == nil || strings.TrimSpace(s.readyFenceDir) == "" {
		return nil
	}
	s.appliedPersistMu.Lock()
	defer s.appliedPersistMu.Unlock()
	active := s.activeIndexesByView()
	s.appliedFenceMu.Lock()
	items := make([]persistedApplied, 0, len(s.appliedFence))
	for key, seq := range s.appliedFence {
		if index, known := active[viewRef{spaceID: key.spaceID, viewID: key.viewID}]; known && index != "" && index != key.indexID {
			delete(s.appliedFence, key)
			continue
		}
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

// activeIndexesByView 返回已登记 View 的当前活动索引。
func (s *Service) activeIndexesByView() map[viewRef]string {
	s.mu.RLock()
	runtimes := make(map[viewRef]*viewRuntime, len(s.views))
	for ref, runtime := range s.views {
		runtimes[ref] = runtime
	}
	s.mu.RUnlock()
	active := make(map[viewRef]string, len(runtimes))
	for ref, runtime := range runtimes {
		if runtime == nil {
			continue
		}
		runtime.mu.Lock()
		active[ref] = runtime.active
		runtime.mu.Unlock()
	}
	return active
}

// setAsideCorrupt 把无法解析的落盘文件改名保留（便于排查）并记日志，让进程按空状态继续启动。
func setAsideCorrupt(path string, cause error) {
	aside := fmt.Sprintf("%s.corrupt-%d", path, time.Now().UnixNano())
	if err := os.Rename(path, aside); err != nil {
		log.Printf("无法解析 %s（%v），改名保留失败：%v", path, cause, err)
		return
	}
	log.Printf("无法解析 %s（%v），已改名为 %s，按空状态启动", path, cause, aside)
}

func (s *Service) loadAppliedFence() error {
	path := filepath.Join(s.readyFenceDir, "applied.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var items []persistedApplied
	if err := json.Unmarshal(raw, &items); err != nil {
		// 围栏丢失只会让就绪事件等到新的行写入才放行，不能因此让 View 角色起不来。
		setAsideCorrupt(path, err)
		return nil
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
	path := filepath.Join(s.readyFenceDir, "pending.json")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var items []persistedPendingReady
	if err := json.Unmarshal(raw, &items); err != nil {
		// 未 ACK 的周期事件会被重投并重新入队；已 ACK 的只能靠排查改名保留的文件补发。
		setAsideCorrupt(path, err)
		return nil
	}
	pending := make([]pendingViewReady, 0, len(items))
	for _, item := range items {
		payload := &storagepb.ViewDataReady{}
		if err := proto.Unmarshal(item.Payload, payload); err != nil {
			log.Printf("View 就绪队列丢弃无法解析的事件 %s（%s/%s）：%v", item.EventID, item.SpaceID, item.ViewID, err)
			continue
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
	s.observeReadyQueueLocked()
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

// atomicWriteFile 先写同目录下的唯一临时文件并 fsync，再改名替换：并发的写者不会互相截断，读者与崩溃后的启动
// 只会看到完整的旧文件或新文件。
func atomicWriteFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	cleanup := func(cause error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return cause
	}
	if _, err := tmp.Write(raw); err != nil {
		return cleanup(err)
	}
	if err := tmp.Sync(); err != nil {
		return cleanup(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Chmod(name, 0o644); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		_ = os.Remove(name)
		return err
	}
	if handle, err := os.Open(dir); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
	return nil
}
