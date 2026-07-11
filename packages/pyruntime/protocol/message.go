package protocol

import (
	"encoding/json"
	"errors"
	"fmt"
)

const VersionV1 = "moox.py/v1"

type MessageType byte

const (
	TypeHello  MessageType = 0x01
	TypeLoad   MessageType = 0x02
	TypeRun    MessageType = 0x03
	TypeResult MessageType = 0x04
	TypeError  MessageType = 0x05
	TypePing   MessageType = 0x06
	TypeDrain  MessageType = 0x07
)

type Encoding string

const (
	EncodingJSON        Encoding = "json"
	EncodingArrowStream Encoding = "arrow_stream"
	EncodingArrowMMap   Encoding = "arrow_mmap"
)

type Hello struct {
	ProtocolVersion string            `json:"protocol_version"`
	WorkerVersion   string            `json:"worker_version"`
	PythonVersion   string            `json:"python_version"`
	RuntimeEnvHash  string            `json:"runtime_env_hash"`
	Encodings       []Encoding        `json:"encodings"`
	Packages        map[string]string `json:"packages,omitempty"`
}

type HelloExpectation struct {
	ProtocolVersion  string
	RuntimeEnvHash   string
	RequiredEncoding Encoding
}

var (
	ErrProtocolMismatch    = errors.New("pyruntime: protocol mismatch")
	ErrRuntimeMismatch     = errors.New("pyruntime: runtime environment mismatch")
	ErrEncodingUnsupported = errors.New("pyruntime: encoding unsupported")
)

func DecodeHello(raw json.RawMessage) (Hello, error) {
	var hello Hello
	if err := json.Unmarshal(raw, &hello); err != nil {
		return Hello{}, fmt.Errorf("decode hello: %w", err)
	}
	return hello, nil
}

func ValidateHello(expect HelloExpectation, got Hello) error {
	if expect.ProtocolVersion != "" && got.ProtocolVersion != expect.ProtocolVersion {
		return fmt.Errorf("%w: expected=%s actual=%s", ErrProtocolMismatch, expect.ProtocolVersion, got.ProtocolVersion)
	}
	if expect.RuntimeEnvHash != "" && got.RuntimeEnvHash != expect.RuntimeEnvHash {
		return fmt.Errorf("%w: expected=%s actual=%s", ErrRuntimeMismatch, expect.RuntimeEnvHash, got.RuntimeEnvHash)
	}
	if expect.RequiredEncoding != "" {
		for _, encoding := range got.Encodings {
			if encoding == expect.RequiredEncoding {
				return nil
			}
		}
		return fmt.Errorf("%w: %s", ErrEncodingUnsupported, expect.RequiredEncoding)
	}
	return nil
}
