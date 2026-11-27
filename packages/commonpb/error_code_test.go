package commonpb

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
)

func TestSpaceNotFoundHasDistinctEnumValue(t *testing.T) {
	if ErrorCode_SPACE_NOT_FOUND == ErrorCode_NOT_FOUND {
		t.Fatalf("SPACE_NOT_FOUND (%d) must not alias NOT_FOUND (%d)", ErrorCode_SPACE_NOT_FOUND, ErrorCode_NOT_FOUND)
	}
	if got := ErrorCode_SPACE_NOT_FOUND.String(); got != "SPACE_NOT_FOUND" {
		t.Fatalf("ErrorCode_SPACE_NOT_FOUND.String() = %q, want %q", got, "SPACE_NOT_FOUND")
	}
	if got := ErrorCode_NOT_FOUND.String(); got != "NOT_FOUND" {
		t.Fatalf("ErrorCode_NOT_FOUND.String() = %q, want %q", got, "NOT_FOUND")
	}

	raw, err := protojson.Marshal(&RetInfo{Code: ErrorCode_SPACE_NOT_FOUND, Msg: "space missing"})
	if err != nil {
		t.Fatalf("protojson.Marshal: %v", err)
	}
	if !strings.Contains(string(raw), `"SPACE_NOT_FOUND"`) {
		t.Fatalf("protojson output %s does not contain %q", raw, "SPACE_NOT_FOUND")
	}

	var decoded RetInfo
	if err := protojson.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("protojson.Unmarshal: %v", err)
	}
	if decoded.GetCode() != ErrorCode_SPACE_NOT_FOUND {
		t.Fatalf("decoded code = %v, want %v", decoded.GetCode(), ErrorCode_SPACE_NOT_FOUND)
	}
}
