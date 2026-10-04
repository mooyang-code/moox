package rpc

import (
	"testing"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestPageFromCommon_ShouldNormalizeBounds(t *testing.T) {
	page, size := pageFromCommon(nil)
	assert.Equal(t, 1, page)
	assert.Equal(t, 50, size)

	page, size = pageFromCommon(&pb.Page{Page: 0, Size: 5000})
	assert.Equal(t, 1, page)
	assert.Equal(t, 1000, size)

	result := pageResult(1, 50, 120)
	assert.True(t, result.GetHasMore())
	assert.Equal(t, uint32(120), result.GetTotal())
}

func TestMetadataHelpers_ShouldParseMixedTypes(t *testing.T) {
	meta := map[string]any{
		"name":    "node-a",
		"count":   float64(3),
		"enabled": true,
		"tags":    []any{"collect.kline"},
	}
	assert.Equal(t, "node-a", metadataString(meta, "name"))
	assert.Equal(t, int32(3), metadataInt32(meta, "count"))
	assert.True(t, metadataBool(meta, "enabled"))
}

func TestJSONHelpers_ShouldMergeAndStringify(t *testing.T) {
	merged := mergeMetadataJSON(`{"a":"1"}`, `{"b":"2"}`)
	assert.Contains(t, merged, `"a":"1"`)
	assert.Contains(t, merged, `"b":"2"`)

	st, err := structpb.NewStruct(map[string]any{"k": "v"})
	assert.NoError(t, err)
	values := structMap(st)
	assert.Equal(t, "v", values["k"])
	assert.Equal(t, "{}", jsonString(nil))
}

func TestFormatTime(t *testing.T) {
	assert.Equal(t, "", formatTime(time.Time{}))
	assert.NotEmpty(t, formatTime(Now()))
}

func TestMetadataHelpers_ExtraBranches(t *testing.T) {
	meta := map[string]any{
		"i": int(7), "i32": int32(8), "i64": int64(9),
		"snum": "12", "sbool": "true",
	}
	assert.Equal(t, "7", metadataString(meta, "i"))
	assert.Equal(t, int32(8), metadataInt32(meta, "i32"))
	assert.Equal(t, int32(9), metadataInt32(meta, "i64"))
	assert.Equal(t, int32(12), metadataInt32(meta, "snum"))
	assert.True(t, metadataBool(meta, "sbool"))
	assert.Equal(t, map[string]any{}, parseJSONMap(""))
	assert.Equal(t, map[string]any{}, parseJSONMap("{"))
}

func TestCompactStrings_ShouldDedupeAndTrim(t *testing.T) {
	out := compactStrings([]string{" a ", "a", "", "b"})
	assert.Equal(t, []string{"a", "b"}, out)
}

func TestFirstString_ReturnsFirstNonEmpty(t *testing.T) {
	assert.Equal(t, "b", firstString("", " b ", "c"))
}
