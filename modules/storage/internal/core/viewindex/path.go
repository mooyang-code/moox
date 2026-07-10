package viewindex

import (
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

type Slot string

const (
	SlotA Slot = "a"
	SlotB Slot = "b"
)

type Ref struct {
	SpaceID string
	ViewID  string
	Slot    Slot
}

func (r Ref) ID() string {
	return ViewIndexID(r.SpaceID, r.ViewID, r.Slot)
}

func ParseViewIndexID(indexID string) (Ref, error) {
	const prefix = "view_s"
	if !strings.HasPrefix(indexID, prefix) {
		return Ref{}, fmt.Errorf("invalid view index ID %q", indexID)
	}
	rest := strings.TrimPrefix(indexID, prefix)
	viewSep := strings.Index(rest, "_v")
	if viewSep <= 0 {
		return Ref{}, fmt.Errorf("invalid view index ID %q", indexID)
	}
	spaceHex := rest[:viewSep]
	rest = rest[viewSep+2:]
	slotSep := strings.LastIndexByte(rest, '_')
	if slotSep <= 0 || slotSep == len(rest)-1 {
		return Ref{}, fmt.Errorf("invalid view index ID %q", indexID)
	}
	viewHex := rest[:slotSep]
	slot := Slot(rest[slotSep+1:])
	if slot != SlotA && slot != SlotB {
		return Ref{}, fmt.Errorf("invalid view index slot %q", slot)
	}
	spaceID, err := decodeIndexIDPart(spaceHex)
	if err != nil {
		return Ref{}, fmt.Errorf("decode space ID: %w", err)
	}
	viewID, err := decodeIndexIDPart(viewHex)
	if err != nil {
		return Ref{}, fmt.Errorf("decode view ID: %w", err)
	}
	if !validLogicalID(spaceID) || !validLogicalID(viewID) {
		return Ref{}, errors.New("view index contains an invalid space_id or view_id")
	}
	ref := Ref{SpaceID: spaceID, ViewID: viewID, Slot: slot}
	if ref.ID() != indexID {
		return Ref{}, fmt.Errorf("view index ID %q is not canonical", indexID)
	}
	return ref, nil
}

func DuckDBPath(root string, ref Ref) string {
	return filepath.Join(root, "duckdb", encodeIndexIDPart(ref.SpaceID), encodeIndexIDPart(ref.ViewID), string(ref.Slot)+".duckdb")
}

func BlevePath(root string, ref Ref) string {
	return filepath.Join(root, "bleve", encodeIndexIDPart(ref.SpaceID), encodeIndexIDPart(ref.ViewID), string(ref.Slot))
}

func decodeIndexIDPart(value string) (string, error) {
	if value == "" {
		return "", errors.New("encoded ID part is empty")
	}
	raw, err := hex.DecodeString(value)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func validLogicalID(value string) bool {
	if strings.TrimSpace(value) == "" || strings.ContainsAny(value, "/\\") {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
