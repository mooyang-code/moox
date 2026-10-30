package viewindex

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// ViewIndexRef 是从索引标识解析出的 View 身份。
type ViewIndexRef struct {
	SpaceID string
	ViewID  string
}

// ViewIndexID 返回 View 唯一的物理索引标识。
func ViewIndexID(spaceID, viewID string) string {
	return fmt.Sprintf("view_s%s_v%s", encode(spaceID), encode(viewID))
}

// ParseViewIndexID 解析 ViewIndexID 生成的标识。
func ParseViewIndexID(id string) (ViewIndexRef, error) {
	var ref ViewIndexRef
	parts := strings.Split(id, "_")
	if len(parts) != 3 || parts[0] != "view" || !strings.HasPrefix(parts[1], "s") || !strings.HasPrefix(parts[2], "v") {
		return ref, errors.New("invalid view index id")
	}
	ref.SpaceID = decode(parts[1][1:])
	ref.ViewID = decode(parts[2][1:])
	if ref.SpaceID == "" || ref.ViewID == "" || ViewIndexID(ref.SpaceID, ref.ViewID) != id {
		return ref, errors.New("invalid view index id")
	}
	return ref, nil
}

func encode(v string) string { return hex.EncodeToString([]byte(v)) }
func decode(v string) string {
	raw, err := hex.DecodeString(v)
	if err != nil {
		return ""
	}
	return string(raw)
}
