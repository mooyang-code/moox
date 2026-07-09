package viewindex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
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
}

type ViewIndexStats struct {
	Exists     bool
	EntryCount int64
	MinVersion string
	MaxVersion string
	SchemaHash string
}

func ViewIndexID(spaceID string, viewID string, slot string) string {
	name := sanitizeResultTableName(fmt.Sprintf("view_%s_%s_%s", spaceID, viewID, slot))
	if name == "" {
		return "view_result_" + slot
	}
	return name
}

func InactiveViewIndexID(spaceID string, viewID string, activeIndexID string) string {
	slotA := ViewIndexID(spaceID, viewID, "a")
	if activeIndexID == slotA {
		return ViewIndexID(spaceID, viewID, "b")
	}
	return slotA
}

func BuildingIndexWritable(view *pb.View) bool {
	return view != nil &&
		view.GetBuildingResult() != "" &&
		view.GetBuildingViewVersion() == view.GetViewVersion() &&
		view.GetBuildStatus() == "building"
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

var invalidTableChar = regexp.MustCompile(`[^A-Za-z0-9_]+`)

func sanitizeResultTableName(raw string) string {
	name := invalidTableChar.ReplaceAllString(raw, "_")
	name = strings.Trim(name, "_")
	if name == "" {
		return ""
	}
	if first := name[0]; (first < 'A' || first > 'Z') && (first < 'a' || first > 'z') && first != '_' {
		name = "view_result_" + name
	}
	return name
}
