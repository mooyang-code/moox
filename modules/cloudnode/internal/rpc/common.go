package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/spacecontext"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"google.golang.org/protobuf/types/known/structpb"
)

func Now() time.Time {
	return time.Now().UTC()
}

func directBatchID(action string) string {
	return fmt.Sprintf("batch-%s-%d", action, Now().UnixNano())
}

func spaceFromContext(ctx context.Context) string {
	spaceID, _ := spacecontext.FromContext(ctx)
	return spaceID
}

func pageFromCommon(page *pb.Page) (int, int) {
	if page == nil {
		return 1, 50
	}
	p, s := int(page.GetPage()), int(page.GetSize())
	if p <= 0 {
		p = 1
	}
	if s <= 0 {
		s = 50
	}
	if s > 1000 {
		s = 1000
	}
	return p, s
}

func pageResult(page, size int, total int64) *pb.PageResult {
	return &pb.PageResult{
		Page:    uint32(page),
		Size:    uint32(size),
		Total:   uint32(total),
		HasMore: int64(page*size) < total,
	}
}

func compactStrings(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func structMap(st *structpb.Struct) map[string]any {
	if st == nil {
		return map[string]any{}
	}
	values := st.AsMap()
	if values == nil {
		return map[string]any{}
	}
	return values
}

func parseJSONMap(raw string) map[string]any {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return map[string]any{}
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(raw), &values); err != nil || values == nil {
		return map[string]any{}
	}
	return values
}

func jsonString(values map[string]any) string {
	if len(values) == 0 {
		return "{}"
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

func mergeMetadataJSON(baseRaw string, updateRaw string) string {
	base := parseJSONMap(baseRaw)
	update := parseJSONMap(updateRaw)
	for key, value := range update {
		base[key] = value
	}
	return jsonString(base)
}

func metadataString(metadata map[string]any, key string) string {
	value, ok := metadata[key]
	if !ok || value == nil {
		return ""
	}
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int32:
		return strconv.Itoa(int(v))
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		return strconv.FormatBool(v)
	default:
		return strings.TrimSpace(fmt.Sprint(v))
	}
}

func metadataInt32(metadata map[string]any, key string) int32 {
	value, ok := metadata[key]
	if !ok || value == nil {
		return 0
	}
	switch v := value.(type) {
	case float64:
		return int32(v)
	case int:
		return int32(v)
	case int32:
		return v
	case int64:
		return int32(v)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 32)
		return int32(n)
	default:
		return 0
	}
}

func metadataBool(metadata map[string]any, key string) bool {
	value, ok := metadata[key]
	if !ok || value == nil {
		return false
	}
	switch v := value.(type) {
	case bool:
		return v
	case string:
		b, _ := strconv.ParseBool(strings.TrimSpace(v))
		return b
	default:
		return false
	}
}

func supportedWorkloadsFromMetadata(metadata map[string]any) string {
	if value, ok := metadata["supported_workloads"].([]any); ok && len(value) > 0 {
		raw, err := json.Marshal(value)
		if err == nil {
			return string(raw)
		}
	}
	if raw := metadataString(metadata, "supported_workloads"); raw != "" {
		return raw
	}
	if bizType := metadataString(metadata, "biz_type"); bizType != "" {
		raw, err := json.Marshal([]string{bizType})
		if err == nil {
			return string(raw)
		}
	}
	return "[]"
}

func reveal(secret string, ok bool) string {
	if ok {
		return secret
	}
	return maskSecret(secret)
}

func maskSecret(secret string) string {
	if secret == "" {
		return ""
	}
	if len(secret) <= 8 {
		return "****"
	}
	return secret[:4] + "****" + secret[len(secret)-4:]
}

func firstString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339)
}
