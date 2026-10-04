package viewindex

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

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
	if parts[3] != string(SlotA) && parts[3] != string(SlotB) {
		return ref, errors.New("invalid view index slot")
	}
	ref.Slot = Slot(parts[3])
	if ref.SpaceID == "" || ref.ViewID == "" || ViewIndexID(ref.SpaceID, ref.ViewID, ref.Slot) != id {
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
func encode(v string) string { return hex.EncodeToString([]byte(v)) }
func decode(v string) string {
	raw, err := hex.DecodeString(v)
	if err != nil {
		return ""
	}
	return string(raw)
}
