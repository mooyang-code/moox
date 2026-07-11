package protocol

import (
	"errors"
	"testing"
)

func TestValidateHelloRejectsRuntimeHashMismatch(t *testing.T) {
	err := ValidateHello(HelloExpectation{ProtocolVersion: VersionV1, RuntimeEnvHash: "expected"}, Hello{ProtocolVersion: VersionV1, RuntimeEnvHash: "actual", Encodings: []Encoding{EncodingJSON}})
	if !errors.Is(err, ErrRuntimeMismatch) {
		t.Fatalf("err=%v", err)
	}
}
