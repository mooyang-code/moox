package viewindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
)

type ViewIndexEngine interface {
	Engine() string
	Prepare(ctx context.Context, indexID string, schema ViewIndexSchema) error
	Write(ctx context.Context, indexID string, batch ViewIndexBatch) error
	Stat(ctx context.Context, indexID string) (ViewIndexStats, error)
	Remove(ctx context.Context, indexID string) error
}

type ViewIndexSchema struct {
	SpaceID     string
	ViewID      string
	ViewVersion uint64
	Engine      string
	Columns     []*pb.ViewColumn
	SchemaHash  string
}

type ViewIndexBatch struct {
	TimeSeriesRows []*pb.TimeSeriesRow
	RecordRows     []*pb.RecordRow
	Columns        []*pb.ViewColumn
	ViewVersion    uint64
	SchemaHash     string
}

type ViewIndexStats struct {
	Exists        bool
	ViewVersion   uint64
	EntryCount    int64
	MinVersion    string
	MaxVersion    string
	SchemaHash    string
	PhysicalBytes uint64
	UpdatedAt     string
	FreeDiskBytes uint64
}

func ViewIndexID(spaceID string, viewID string, slot Slot) string {
	return fmt.Sprintf("view_s%s_v%s_%s", encodeIndexIDPart(spaceID), encodeIndexIDPart(viewID), normalizeSlot(slot))
}

func InactiveViewIndexID(spaceID string, viewID string, activeIndexID string) string {
	ref, err := ParseViewIndexID(activeIndexID)
	if err == nil && ref.SpaceID == spaceID && ref.ViewID == viewID && ref.Slot == SlotA {
		return ViewIndexID(spaceID, viewID, SlotB)
	}
	return ViewIndexID(spaceID, viewID, SlotA)
}

func encodeIndexIDPart(value string) string {
	encoded := hex.EncodeToString([]byte(value))
	if encoded == "" {
		return "00"
	}
	return encoded
}

func normalizeSlot(slot Slot) Slot {
	switch Slot(strings.ToLower(strings.TrimSpace(string(slot)))) {
	case SlotB:
		return SlotB
	default:
		return SlotA
	}
}

func BuildIndexWritable(view *pb.View) bool {
	if view == nil || view.GetIndexBuild() == nil {
		return false
	}
	build := view.GetIndexBuild()
	if build.GetIndexId() == "" || build.GetTargetViewVersion() != view.GetViewVersion() ||
		(build.GetState() != pb.ViewIndexBuild_BUILDING && build.GetState() != pb.ViewIndexBuild_CATCHING_UP) ||
		!strings.EqualFold(strings.TrimSpace(build.GetEngine()), strings.TrimSpace(view.GetEngine())) {
		return false
	}
	ref, err := ParseViewIndexID(build.GetIndexId())
	if err != nil || ref.SpaceID != view.GetSpaceId() || ref.ViewID != view.GetViewId() {
		return false
	}
	currentSchema := ViewIndexSchema{
		SpaceID: view.GetSpaceId(), ViewID: view.GetViewId(), Engine: view.GetEngine(), Columns: view.GetColumns(),
	}
	wantHash := HashViewIndexSchema(currentSchema)
	if build.GetSchemaHash() == "" || build.GetSchemaHash() != wantHash {
		return false
	}
	buildSchema := currentSchema
	buildSchema.Columns = build.GetColumns()
	return HashViewIndexSchema(buildSchema) == wantHash
}

// WritableIndexIDs returns the active index and, when its durable build is
// current and write-safe, the warming index. Returned IDs are deduplicated.
func WritableIndexIDs(view *pb.View) []string {
	if view == nil {
		return nil
	}
	out := make([]string, 0, 2)
	active := strings.TrimSpace(view.GetActiveIndexId())
	if active != "" {
		out = append(out, active)
	}
	if BuildIndexWritable(view) {
		buildID := strings.TrimSpace(view.GetIndexBuild().GetIndexId())
		if buildID != "" && buildID != active {
			out = append(out, buildID)
		}
	}
	return out
}

func HashViewIndexSchema(schema ViewIndexSchema) string {
	type columnShape struct {
		Name       string `json:"name"`
		OriginID   string `json:"origin_id"`
		OriginType int32  `json:"origin_type"`
		ValueType  int32  `json:"value_type"`
		SortOrder  uint32 `json:"sort_order"`
	}
	shape := struct {
		SpaceID string        `json:"space_id"`
		ViewID  string        `json:"view_id"`
		Engine  string        `json:"engine"`
		Columns []columnShape `json:"columns"`
	}{
		SpaceID: schema.SpaceID,
		ViewID:  schema.ViewID,
		Engine:  schema.Engine,
	}
	for _, column := range schema.Columns {
		shape.Columns = append(shape.Columns, columnShape{
			Name:       column.GetColumnName(),
			OriginID:   column.GetOriginId(),
			OriginType: int32(column.GetOriginType()),
			ValueType:  int32(column.GetValueType()),
			SortOrder:  column.GetSortOrder(),
		})
	}
	raw, _ := json.Marshal(shape)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
