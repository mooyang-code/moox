// Package hostbundle 定义主机身份包的公开清单；私钥内容不出现在这些类型里。
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

type File struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// ClientMetadata inventories an immutable EventBus export. By default it holds
// client roles and the public CA only. Server marks the export for the host that
// runs the broker: it additionally carries server.pem, server-key.pem and
// users.yaml, and only the eventbus unit may consume such an export.
// CA is the SHA256 of the original public ca.pem bytes, not a signing key.
type ClientMetadata struct {
	Version   int      `json:"version"`
	Status    string   `json:"status"`
	OutputDir string   `json:"output_dir"`
	Roles     []string `json:"roles"`
	CA        string   `json:"ca_sha256"`
	Server    bool     `json:"server,omitempty"`
	Files     []File   `json:"files"`
}

// ServerFiles are the extra files of a Server export, in sorted order.
var ServerFiles = []string{"server-key.pem", "server.pem", "users.yaml"}

// 身份包里的文件名。host.key、scf-collector.key 只在这次新签发了对应私钥时才有。
const (
	HostKeyFile         = "host.key"
	HostCertificateFile = "host.crt"
	HostTLSKeyFile      = "host.tls.key"
	CAFile              = "moox-ca.crt"
	SCFCollectorKeyFile = "scf-collector.key"
)

// Metadata 是一台主机身份包的公开清单：主机签名私钥（新签发时）、主机证书、根证书；控制机另有 scf-collector 的私钥。
// 公钥都是 base64 的 32 字节 ed25519 公钥，是 Admin 里登记的那一把。
type Metadata struct {
	Version       int    `json:"version"`
	HostID        string `json:"host_id"`
	ControlHostID string `json:"control_host_id"`
	BundleDir     string `json:"bundle_dir"`
	// SignPublicKey 是这台主机登记的签名公钥；SignKeyIssued 为真表示这次新生成了私钥（包里有 host.key），
	// 为假表示目标机上的私钥和登记的一致、原样沿用。
	SignPublicKey string `json:"sign_public_key"`
	SignKeyIssued bool   `json:"sign_key_issued"`
	// SCFCollectorPublicKey 只在部署了 collector 的主机（控制机）上有。
	SCFCollectorPublicKey string `json:"scf_collector_public_key,omitempty"`
	SCFCollectorKeyIssued bool   `json:"scf_collector_key_issued,omitempty"`
	// OperatorPublicKey 是 Admin 里登记的 moox-cli 公钥，只有 bootstrap 返回。
	OperatorPublicKey string          `json:"operator_public_key,omitempty"`
	CA                CAInfo          `json:"ca"`
	Certificate       CertificateInfo `json:"certificate"`
	Files             []File          `json:"files"`
}
