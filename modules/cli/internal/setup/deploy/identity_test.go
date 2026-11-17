package deploy

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/identitybundle"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/stretchr/testify/require"
)

// fakeAdmin 模拟控制机上的 moox-admin-cli：登记公钥、按“目标机上的公钥和登记的一致就沿用”签发身份包。
type fakeAdmin struct {
	t        *testing.T
	cfg      *topology.Config
	ca       *x509.Certificate
	caKey    *ecdsa.PrivateKey
	hosts    map[string]string // 主机 → 登记的签名公钥
	operator string            // 登记的 moox-cli 公钥
	scf      string
	// external 是登记着的外部身份公钥（keys list 返回它们与 moox-cli）。
	external map[string]string
	files    map[string][]byte
	bundles  int
}

func newFakeAdmin(t *testing.T, cfg *topology.Config) *fakeAdmin {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "MooX CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	require.NoError(t, err)
	ca, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	return &fakeAdmin{t: t, cfg: cfg, ca: ca, caKey: key, hosts: map[string]string{}, files: map[string][]byte{}}
}

func sha(raw []byte) string { sum := sha256.Sum256(raw); return hex.EncodeToString(sum[:]) }

func flagValue(argv []string, name string) string {
	if i := slices.Index(argv, name); i >= 0 && i+1 < len(argv) {
		return argv[i+1]
	}
	return ""
}

func (a *fakeAdmin) newKey() (public string, file []byte) {
	key, err := gatewayauth.GenerateSigningKey()
	require.NoError(a.t, err)
	return identitybundle.PublicKey(key), []byte(gatewayauth.EncodeSigningKey(key) + "\n")
}

// issue 模拟 bootstrap 与 host-bundle 的签发：返回清单，文件放进 files 供下载。
func (a *fakeAdmin) issue(hostID string, argv []string) hostbundle.Metadata {
	host := a.cfg.HostByID(hostID)
	a.bundles++
	directory := fmt.Sprintf("%s/bundles/host-bundle-%d", flagValue(argv, "--output-dir"), a.bundles)
	metadata := hostbundle.Metadata{Version: 2, HostID: hostID, ControlHostID: a.cfg.ControlHost().Name, BundleDir: directory,
		CA: hostbundle.CAInfo{CertificateInfo: hostbundle.CertificateInfo{SHA256: sha(a.ca.Raw), Serial: a.ca.SerialNumber.Text(16), NotAfter: a.ca.NotAfter.UTC()}}}
	files := map[string][]byte{hostbundle.CAFile: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: a.ca.Raw})}
	current := flagValue(argv, "--current-sign-public-key")
	if slices.Contains(argv, "--rotate-sign-key") || current == "" || current != a.hosts[hostID] {
		a.hosts[hostID], files[hostbundle.HostKeyFile] = a.newKey()
		metadata.SignKeyIssued = true
	}
	metadata.SignPublicKey = a.hosts[hostID]
	if slices.Contains(a.cfg.Deployments[hostID], "collector") {
		if current := flagValue(argv, "--current-scf-collector-public-key"); current == "" || current != a.scf {
			a.scf, files[hostbundle.SCFCollectorKeyFile] = a.newKey()
			metadata.SCFCollectorKeyIssued = true
		}
		metadata.SCFCollectorPublicKey = a.scf
	}
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(a.t, err)
	leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(100 + a.bundles)), Subject: pkix.Name{CommonName: hostID}, DNSNames: []string{hostID, "localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	if host.Reachability != "" {
		leaf.ExtKeyUsage = append(leaf.ExtKeyUsage, x509.ExtKeyUsageClientAuth)
	}
	for _, address := range []string{host.Address, host.PrivateAddress} {
		if ip := net.ParseIP(address); ip != nil {
			leaf.IPAddresses = append(leaf.IPAddresses, ip)
		} else if address != "" {
			leaf.DNSNames = append(leaf.DNSNames, address)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, a.ca, &leafKey.PublicKey, a.caKey)
	require.NoError(a.t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(a.t, err)
	metadata.Certificate = hostbundle.CertificateInfo{SHA256: sha(der), Serial: cert.SerialNumber.Text(16), NotAfter: cert.NotAfter.UTC()}
	files[hostbundle.HostCertificateFile] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	pkcs8, err := x509.MarshalPKCS8PrivateKey(leafKey)
	require.NoError(a.t, err)
	files[hostbundle.HostTLSKeyFile] = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	for name, raw := range files {
		metadata.Files = append(metadata.Files, hostbundle.File{Path: name, SHA256: sha(raw), Size: int64(len(raw))})
		a.files[path.Join(directory, name)] = raw
	}
	slices.SortFunc(metadata.Files, func(x, y hostbundle.File) int { return strings.Compare(x.Path, y.Path) })
	return metadata
}

// fakeHost 是一台主机上的 SSH 连接：目标机上现有的私钥由 keys 给出，控制机另外能执行 moox-admin-cli。
type fakeHost struct {
	t     *testing.T
	admin *fakeAdmin
	keys  map[string][]byte // secrets/ 下的文件名 → 私钥文件内容
	runs  [][]string
}

func (h *fakeHost) Check(context.Context) error { return nil }
func (h *fakeHost) Close() error                { return nil }
func (h *fakeHost) ForwardLocal(context.Context, string) (net.Listener, error) {
	return nil, errors.New("unsupported")
}
func (h *fakeHost) Upload(context.Context, io.Reader, int64, string, fs.FileMode) error { return nil }
func (h *fakeHost) Download(_ context.Context, source string, out io.Writer) (int64, error) {
	raw, ok := h.admin.files[source]
	if !ok {
		return 0, fs.ErrNotExist
	}
	return io.Copy(out, bytes.NewReader(raw))
}

func (h *fakeHost) Run(_ context.Context, argv []string, _ io.Reader) (setupssh.Result, error) {
	h.runs = append(h.runs, argv)
	reply := func(value any) (setupssh.Result, error) {
		raw, err := json.Marshal(value)
		require.NoError(h.t, err)
		return setupssh.Result{Stdout: string(raw) + "\n"}, nil
	}
	switch path.Base(argv[0]) {
	case "moox-host-gateway-cli":
		// 私钥不离开目标机：这里只回公钥。
		key, err := gatewayauth.ParseSigningKey(string(h.keys[path.Base(flagValue(argv, "--key-file"))]))
		if err != nil {
			return setupssh.Result{Stderr: "no key", ExitCode: 1}, errors.New("exit 1")
		}
		return setupssh.Result{Stdout: identitybundle.PublicKey(key) + "\n"}, nil
	case "moox-admin-cli":
		require.NotNil(h.t, h.admin, "只有控制机上有 moox-admin-cli")
		switch argv[1] {
		case "bootstrap":
			if h.admin.operator == "" {
				h.admin.operator = flagValue(argv, "--operator-public-key")
			}
			metadata := h.admin.issue(h.admin.cfg.ControlHost().Name, argv)
			metadata.OperatorPublicKey = h.admin.operator
			h.admin.rewriteManifest(metadata)
			return reply(metadata)
		case "host-bundle":
			metadata := h.admin.issue(flagValue(argv, "--host-id"), argv)
			h.admin.rewriteManifest(metadata)
			return reply(metadata)
		case "keys":
			require.Equal(h.t, "list", argv[2])
			identities := []IdentityKey{{Identity: "moox-cli", PublicKey: h.admin.operator}}
			for name, public := range h.admin.external {
				identities = append(identities, IdentityKey{Identity: name, PublicKey: public})
			}
			return reply(map[string]any{"status": "ok", "identities": identities})
		default:
			return reply(map[string]string{"status": "ok"})
		}
	}
	return setupssh.Result{}, nil
}

func (a *fakeAdmin) rewriteManifest(metadata hostbundle.Metadata) {
	raw, err := json.Marshal(metadata)
	require.NoError(a.t, err)
	a.files[path.Join(metadata.BundleDir, "bundle.json")] = append(raw, '\n')
}

func (h *fakeHost) adminCall(command string) []string {
	for _, argv := range h.runs {
		if path.Base(argv[0]) == "moox-admin-cli" && argv[1] == command {
			return argv
		}
	}
	return nil
}

type identityFixture struct {
	t        *testing.T
	cfg      *topology.Config
	catalog  servicecatalog.Catalog
	admin    *fakeAdmin
	control  *fakeHost
	operator string
	synced   []string
}

func newIdentityFixture(t *testing.T) *identityFixture {
	t.Helper()
	// 示例配置里各主机的 SSH 私钥写的是 ~/.ssh/moox_ed25519：家目录指到临时目录并放一份假私钥，测试不读这台电脑上真的私钥。
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ssh"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".ssh", "moox_ed25519"), []byte("fixture-ssh-private-key\n"), 0o600))
	cfg := exampleConfig(t, "")
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	admin := newFakeAdmin(t, cfg)
	operator, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return &identityFixture{t: t, cfg: cfg, catalog: catalog, admin: admin, control: &fakeHost{t: t, admin: admin, keys: map[string][]byte{}}, operator: filepath.Join(operator, "moox")}
}

func (f *identityFixture) options(host string) Options {
	return Options{Config: f.cfg, Secrets: &topology.Secrets{Admin: topology.Admin{Username: "admin", Password: "fixture-password-123"}}, HostID: host, OperatorDirectory: f.operator,
		Dial:     func(context.Context, topology.Host) (setupssh.Client, error) { return f.control, nil },
		SyncHost: func(_ context.Context, hostID string) error { f.synced = append(f.synced, hostID); return nil }}
}

// run 取一台主机的身份材料。选哪些组件只决定要不要一并取 EventBus 角色文件；
// 这里选不带 EventBus 角色的第三方程序，只看主机身份包这一部分。
func (f *identityFixture) run(options Options, target *fakeHost, fresh bool) (Identity, issuedKeys, error) {
	return options.identity(context.Background(), target, f.catalog, f.cfg.HostByID(options.HostID), f.cfg.ControlHost(), []string{"seaweedfs"}, fresh)
}

func (f *identityFixture) operatorKey() ed25519.PrivateKey {
	key, _, err := identitybundle.EnsureSigningKeyFile(filepath.Join(f.operator, identitybundle.OperatorKeyFile))
	require.NoError(f.t, err)
	return key
}

// 控制机：bootstrap 登记这台电脑的 moox-cli 公钥，签发主机签名私钥与 scf-collector 的私钥；之后目标机上的私钥和登记的一致就不再签发。
func TestControlIdentityRegistersOperatorAndReusesKeysOnRedeploy(t *testing.T) {
	f := newIdentityFixture(t)
	control := f.cfg.ControlHost().Name
	_, _, err := f.run(f.options(control), f.control, true)
	require.ErrorContains(t, err, "operator_key_missing", "没有先运行 setup operator")

	operator := f.operatorKey()
	identity, issued, err := f.run(f.options(control), f.control, true)
	require.NoError(t, err)
	require.True(t, issued.hostKey)
	bootstrap := f.control.adminCall("bootstrap")
	require.Equal(t, identitybundle.PublicKey(operator), flagValue(bootstrap, "--operator-public-key"))
	require.NotContains(t, bootstrap, "--current-sign-public-key", "全新的主机上没有私钥")
	require.Equal(t, f.admin.hosts[control], identity.SignPublicKey)
	require.Equal(t, f.admin.scf, identity.SCFCollectorPublicKey)
	for _, name := range []string{"host.key", "scf-collector.key", "host.crt", "host.tls.key", "moox-ca.crt"} {
		require.Contains(t, identity.Secrets, name)
	}
	// 这台电脑记下了控制机的根证书与指纹，私钥没有被动过。
	pin, err := identitybundle.ReadOperatorPin(f.operator)
	require.NoError(t, err)
	require.Equal(t, identitybundle.OperatorPin{ControlHostID: control, CA: sha(f.admin.ca.Raw)}, pin)
	require.Equal(t, identitybundle.PublicKey(operator), identitybundle.PublicKey(f.operatorKey()))

	// 重新部署：目标机上已经有这两把私钥，公钥传给控制机，对得上就不换；包里没有私钥文件。
	f.control.keys = map[string][]byte{"host.key": identity.Secrets["host.key"], "scf-collector.key": identity.Secrets["scf-collector.key"]}
	f.control.runs = nil
	again, issued, err := f.run(f.options(control), f.control, false)
	require.NoError(t, err)
	require.False(t, issued.hostKey)
	bootstrap = f.control.adminCall("bootstrap")
	require.Equal(t, identity.SignPublicKey, flagValue(bootstrap, "--current-sign-public-key"))
	require.Equal(t, identity.SCFCollectorPublicKey, flagValue(bootstrap, "--current-scf-collector-public-key"))
	require.Equal(t, identity.SignPublicKey, again.SignPublicKey)
	require.NotContains(t, again.Secrets, "host.key")
	require.NotContains(t, again.Secrets, "scf-collector.key")
	require.Contains(t, again.Secrets, "host.crt", "主机证书每次重新签发")

	// 控制机的签名密钥不支持单独更换。
	rotate := f.options(control)
	rotate.RotateHostKey = true
	_, _, err = f.run(rotate, f.control, false)
	require.ErrorContains(t, err, "rotate_unsupported")
}

// 控制机上登记的是另一台电脑的公钥：不悄悄继续，提示用 setup operator --register 换过来。
func TestControlIdentityRejectsAnotherComputersOperatorKey(t *testing.T) {
	f := newIdentityFixture(t)
	f.operatorKey()
	f.admin.operator, _ = f.admin.newKey()
	_, _, err := f.run(f.options(f.cfg.ControlHost().Name), f.control, false)
	require.ErrorContains(t, err, "operator_key_mismatch")
	require.ErrorContains(t, err, "setup operator --register")
}

// 其余主机：先同步登记，再让控制机签发；身份包按这台电脑记下的 CA 校验；--rotate-host-key 换掉主机密钥。
func TestHostIdentityIsIssuedByControlAndVerifiedAgainstPinnedCA(t *testing.T) {
	f := newIdentityFixture(t)
	f.operatorKey()
	storage := &fakeHost{t: t, keys: map[string][]byte{}}
	_, _, err := f.run(f.options("storage"), storage, true)
	require.ErrorContains(t, err, "control_not_ready", "还没部署过控制机，这台电脑没有它的根证书指纹")
	_, _, err = f.run(f.options(f.cfg.ControlHost().Name), f.control, true)
	require.NoError(t, err)

	f.control.runs = nil
	identity, issued, err := f.run(f.options("storage"), storage, true)
	require.NoError(t, err)
	require.True(t, issued.hostKey)
	require.Equal(t, []string{"storage"}, f.synced, "先把这台主机的登记同步到 Admin")
	require.Equal(t, f.admin.hosts["storage"], identity.SignPublicKey)
	require.Empty(t, identity.SCFCollectorPublicKey)
	require.NotEqual(t, f.admin.hosts[f.cfg.ControlHost().Name], identity.SignPublicKey, "每台主机一把")
	for _, argv := range storage.runs {
		require.Equal(t, "sign-public-key", argv[1], "在目标机上只算公钥，私钥没有被读走")
	}

	// 例行升级不换密钥。
	storage.keys["host.key"] = identity.Secrets["host.key"]
	again, issued, err := f.run(f.options("storage"), storage, false)
	require.NoError(t, err)
	require.False(t, issued.hostKey)
	require.NotContains(t, again.Secrets, "host.key")
	// 显式更换：新公钥替换旧的。
	rotate := f.options("storage")
	rotate.RotateHostKey = true
	f.control.runs = nil
	rotated, issued, err := f.run(rotate, storage, false)
	require.NoError(t, err)
	require.True(t, issued.hostKey)
	require.Contains(t, f.control.adminCall("host-bundle"), "--rotate-sign-key")
	require.NotEqual(t, identity.SignPublicKey, rotated.SignPublicKey)
	require.Equal(t, rotated.SignPublicKey, f.admin.hosts["storage"])

	// 控制机换成了另一套 CA（比如连到了别的部署）：身份包按记下的指纹校验，不通过。
	other := newFakeAdmin(t, f.cfg)
	other.hosts, other.operator = f.admin.hosts, f.admin.operator
	f.control.admin = other
	_, _, err = f.run(f.options("storage"), storage, false)
	require.ErrorContains(t, err, "identity_invalid")
}
