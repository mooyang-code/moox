package mooxpb

import (
	"encoding/json"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
)

func TestPageResultTotalJSONIsNumber(t *testing.T) {
	raw, err := protojson.Marshal(&ListNodesRsp{PageResult: &PageResult{Total: 10}})
	if err != nil {
		t.Fatalf("marshal ListNodesRsp: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	pageResult, ok := payload["pageResult"].(map[string]any)
	if !ok {
		t.Fatalf("pageResult missing or invalid: %#v", payload["pageResult"])
	}
	if _, ok := pageResult["total"].(float64); !ok {
		t.Fatalf("pageResult.total must be a JSON number, got %T (%#v)", pageResult["total"], pageResult["total"])
	}
}
