// Package hostbundle defines the public metadata of issued host identity material.
// Private key contents never belong in this wire contract.
package hostbundle

import "time"

// Topology is normalized deployment input. It may carry host SSH credentials
// (Host.SSH), so the file is handled as a private file and never published.
type Topology struct {
	Version       int    `json:"version"`
	ControlHostID string `json:"control_host_id"`
	Hosts         []Host `json:"hosts"`
}

// SSH 是控制台终端登录这台主机用的信息；Port 为 0（或整体缺省）表示控制台不能 SSH。
// AuthType 为 password 或 private_key，Secret 是对应的密码或私钥内容，Passphrase 是私钥口令。
type SSH struct {
	Port       int    `json:"port"`
	User       string `json:"user"`
	AuthType   string `json:"auth_type"`
	Secret     string `json:"secret"`
	Passphrase string `json:"passphrase,omitempty"`
}

type Host struct {
	HostID         string   `json:"host_id"`
	Label          string   `json:"label"`
	Address        string   `json:"address"`
	PrivateAddress string   `json:"private_address"`
	Region         string   `json:"region"`
	Description    string   `json:"description"`
	Components     []string `json:"components"`
	// Reachability 为空表示可直达，outbound 表示只能出站。
	Reachability string `json:"reachability,omitempty"`
	SSH          *SSH   `json:"ssh,omitempty"`
}

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

// ClientMetadata inventories an immutable EventBus client-only export.
// CA is the SHA256 of the original public ca.pem bytes, not a signing key.
type ClientMetadata struct {
	Version   int      `json:"version"`
	Status    string   `json:"status"`
	OutputDir string   `json:"output_dir"`
	Roles     []string `json:"roles"`
	CA        string   `json:"ca_sha256"`
	Files     []File   `json:"files"`
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
