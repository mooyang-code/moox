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
		return errors.New("View 服务未初始化")
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
	// 崩溃时写到一半的临时文件不会再被改名，启动时清掉，免得逐渐累积。
	if leftovers, err := filepath.Glob(filepath.Join(s.readyFenceDir, "*.tmp-*")); err == nil {
		for _, leftover := range leftovers {
			_ = os.Remove(leftover)
		}
	}
	if err := s.loadAppliedFence(); err != nil {
		return err
	}
	return s.loadPendingReady()
}

// appliedPersistInterval 是写入围栏两次落盘之间的最短间隔。
var appliedPersistInterval = time.Second

// flushAppliedFence 把标脏的写入围栏落盘并 fsync。落盘不在行处理的热路径上（后台每秒合并一次），fsync 的代价不会让
// 分区排队；不 fsync 的话，部分文件系统掉电后改名留下空文件，启动时整份围栏被当作损坏清空。后台循环与停止消费先后
// 调用，落盘串行：快照在持有 appliedPersistMu 时取，后写入的文件总是更新的快照。A/B 槽位名在重建时复用，每个 View
// 至多两个索引的键，不需要清理（已删除 View 的少量键会一直留着）。
func (s *Service) flushAppliedFence() error {
	if s == nil || strings.TrimSpace(s.readyFenceDir) == "" {
		return nil
	}
	s.appliedPersistMu.Lock()
	defer s.appliedPersistMu.Unlock()
	s.appliedFenceMu.Lock()
	if !s.appliedDirty {
		s.appliedFenceMu.Unlock()
		return nil
	}
	items := make([]persistedApplied, 0, len(s.appliedFence))
	for key, seq := range s.appliedFence {
		items = append(items, persistedApplied{
			SpaceID: key.spaceID, ViewID: key.viewID, IndexID: key.indexID,
			NodeID: key.nodeID, StoreID: key.storeID, Sequence: seq,
		})
	}
	s.appliedDirty = false
	s.appliedFenceMu.Unlock()
	raw, err := json.Marshal(items)
	if err == nil {
		err = replaceFile(filepath.Join(s.readyFenceDir, "applied.json"), raw, true)
	}
	if err != nil {
		s.appliedFenceMu.Lock()
		s.appliedDirty = true
		s.appliedFenceMu.Unlock()
		return err
	}
	return nil
}

// persistAppliedFenceLogged 落盘写入围栏并记录失败与恢复（按类别去重）。
func (s *Service) persistAppliedFenceLogged() {
	if err := s.flushAppliedFence(); err != nil {
		s.readyIssue(readyIssueFence, "View 写入围栏落盘失败：%v", err)
		return
	}
	s.readyResolved(readyIssueFence, "View 写入围栏落盘已恢复")
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
	return replaceFile(filepath.Join(s.readyFenceDir, "pending.json"), raw, true)
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

// replaceFile 先写同目录下的唯一临时文件，再改名替换：并发的写者不会互相截断，读者只会看到完整的旧文件或新文件。
// durable 为 true 时改名前后都 fsync，断电后也不会丢掉已确认的内容（就绪队列）；写入围栏丢了只会晚放行，不付这个代价。
func replaceFile(path string, raw []byte, durable bool) error {
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
	if durable {
		if err := tmp.Sync(); err != nil {
			return cleanup(err)
		}
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
	if !durable {
		return nil
	}
	if handle, err := os.Open(dir); err == nil {
		_ = handle.Sync()
		_ = handle.Close()
	}
	return nil
}
