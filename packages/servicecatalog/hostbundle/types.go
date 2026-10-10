// Package hostbundle defines the public metadata of issued host identity material.
// Private key contents never belong in this wire contract.
package hostbundle

import "time"

type CertificateInfo struct {
	SHA256   string    `json:"sha256"`
	Serial   string    `json:"serial"`
	NotAfter time.Time `json:"not_after"`
}
type CAInfo struct {
	CertificateInfo
	Created bool `json:"created"`
}

type Credential struct {
	Caller  string `json:"caller"`
	KeyID   string `json:"key_id"`
	KeyFile string `json:"key_file"`
}

// OperatorConfig is the public YAML exported beside the bootstrap operator key.
// Keeping this wire type here avoids importing an RPC client into host tools.
type OperatorConfig struct {
	Caller  string `yaml:"caller"`
	KeyID   string `yaml:"key_id"`
	KeyFile string `yaml:"key_file"`
}

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

type Metadata struct {
	Version          int             `json:"version"`
	HostID           string          `json:"host_id"`
	ControlHostID    string          `json:"control_host_id"`
	BundleDir        string          `json:"bundle_dir"`
	ConfigFile       string          `json:"config_file"`
	VerificationFile string          `json:"verification_file,omitempty"`
	ExpectedHash     string          `json:"expected_hash"`
	CA               CAInfo          `json:"ca"`
	Certificate      CertificateInfo `json:"certificate"`
	Credentials      []Credential    `json:"credentials"`
	Files            []File          `json:"files"`
}
