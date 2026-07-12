package protocol

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateHelloRejectsRuntimeHashMismatch(t *testing.T) {
	err := ValidateHello(HelloExpectation{ProtocolVersion: VersionV1, RuntimeEnvHash: "expected"}, Hello{ProtocolVersion: VersionV1, RuntimeEnvHash: "actual", Encodings: []Encoding{EncodingJSON}})
	if !errors.Is(err, ErrRuntimeMismatch) {
		t.Fatalf("err=%v", err)
	}
}

func TestValidateHelloRejectsProtocolMismatch(t *testing.T) {
	err := ValidateHello(
		HelloExpectation{ProtocolVersion: VersionV1},
		Hello{ProtocolVersion: "other", Encodings: []Encoding{EncodingJSON}},
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrProtocolMismatch)
}

func TestValidateHelloRejectsUnsupportedEncoding(t *testing.T) {
	err := ValidateHello(
		HelloExpectation{ProtocolVersion: VersionV1, RequiredEncoding: EncodingArrowStream},
		Hello{ProtocolVersion: VersionV1, Encodings: []Encoding{EncodingJSON}},
	)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrEncodingUnsupported)
}

func TestValidateHelloAcceptsRequiredEncoding(t *testing.T) {
	err := ValidateHello(
		HelloExpectation{ProtocolVersion: VersionV1, RequiredEncoding: EncodingJSON},
		Hello{ProtocolVersion: VersionV1, Encodings: []Encoding{EncodingJSON, EncodingArrowStream}},
	)
	require.NoError(t, err)
}

func TestDecodeHello_ParsesPayload(t *testing.T) {
	raw, err := json.Marshal(Hello{ProtocolVersion: VersionV1, WorkerVersion: "1", Encodings: []Encoding{EncodingJSON}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeHello(raw)
	if err != nil || got.ProtocolVersion != VersionV1 || got.WorkerVersion != "1" {
		t.Fatalf("hello=%+v err=%v", got, err)
	}
	_, err = DecodeHello(json.RawMessage(`{`))
	if err == nil {
		t.Fatal("expected decode error")
	}
}
