package viewindex

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"

	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
)

// ActiveHandle is immutable after publication and can therefore be held by a
// query for the whole request without observing a slot switch halfway through.
type ActiveHandle struct {
	IndexID  string
	Slot     Slot
	Revision uint64
	Columns  []*pb.ViewColumn
}

type ViewWriteTargets struct {
	Active *ActiveHandle
	New    *ActiveHandle
}

type SlotCoordinator struct {
	mu      sync.RWMutex
	targets ViewWriteTargets
}

func NewSlotCoordinator(active *ActiveHandle) *SlotCoordinator {
	return &SlotCoordinator{targets: ViewWriteTargets{Active: active}}
}
func (c *SlotCoordinator) Targets() ViewWriteTargets {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.targets
}
func (c *SlotCoordinator) BeginBuild(newHandle *ActiveHandle) error {
	if newHandle == nil {
		return errors.New("new handle is required")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.targets.New != nil {
		return errors.New("view build is already running")
	}
	c.targets.New = newHandle
	return nil
}
func (c *SlotCoordinator) FailBuild() { c.mu.Lock(); c.targets.New = nil; c.mu.Unlock() }
func (c *SlotCoordinator) AtomicSwitch(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.targets.New == nil {
		return errors.New("no new view to switch")
	}
	c.targets.Active = c.targets.New
	c.targets.New = nil
	return nil
}

type ViewIndexRef struct {
	SpaceID, ViewID string
	Slot            Slot
}

func ViewIndexID(spaceID, viewID string, slot Slot) string {
	return fmt.Sprintf("view_s%s_v%s_%s", encode(spaceID), encode(viewID), normalizeSlot(slot))
}
func ParseViewIndexID(id string) (ViewIndexRef, error) {
	var ref ViewIndexRef
	parts := strings.Split(id, "_")
	if len(parts) != 4 || parts[0] != "view" || !strings.HasPrefix(parts[1], "s") || !strings.HasPrefix(parts[2], "v") {
		return ref, errors.New("invalid view index id")
	}
	ref.SpaceID = decode(parts[1][1:])
	ref.ViewID = decode(parts[2][1:])
	ref.Slot = normalizeSlot(Slot(parts[3]))
	if ref.SpaceID == "" || ref.ViewID == "" {
		return ref, errors.New("invalid view index id")
	}
	return ref, nil
}
func normalizeSlot(s Slot) Slot {
	if strings.EqualFold(string(s), string(SlotB)) {
		return SlotB
	}
	return SlotA
}
func InactiveViewIndexID(spaceID, viewID, active string) string {
	if ref, e := ParseViewIndexID(active); e == nil && ref.Slot == SlotA {
		return ViewIndexID(spaceID, viewID, SlotB)
	}
	return ViewIndexID(spaceID, viewID, SlotA)
}
func DuckDBPath(root string, ref ViewIndexRef) string {
	return filepath.Join(root, "duckdb", ViewIndexID(ref.SpaceID, ref.ViewID, ref.Slot)+".duckdb")
}
func BlevePath(root string, ref ViewIndexRef) string {
	return filepath.Join(root, "bleve", ViewIndexID(ref.SpaceID, ref.ViewID, ref.Slot))
}
func encode(v string) string { return hex.EncodeToString([]byte(v)) }
func decode(v string) string {
	raw, err := hex.DecodeString(v)
	if err != nil {
		return ""
	}
	return string(raw)
}
