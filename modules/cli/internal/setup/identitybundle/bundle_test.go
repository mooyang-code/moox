package identitybundle

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayauth/authtest"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
	"github.com/stretchr/testify/require"
)

type fixture struct {
	options  Options
	metadata hostbundle.Metadata
	files    map[string][]byte
	ca       *x509.Certificate
	caKey    *ecdsa.PrivateKey
	leaf     *x509.Certificate
	leafKey  *ecdsa.PrivateKey
}

func privateParent(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	require.NoError(t, os.Chmod(dir, 0o700))
	return dir
}

func certificateInfo(cert *x509.Certificate) hostbundle.CertificateInfo {
	return hostbundle.CertificateInfo{SHA256: digest(cert.Raw), Serial: cert.SerialNumber.Text(16), NotAfter: cert.NotAfter.UTC()}
}

func keyFile(label string) []byte {
	return []byte(gatewayauth.EncodeSigningKey(authtest.Key(label)) + "\n")
}

// newFixture 造一份新签发的身份包：host 的主机签名私钥、证书与根证书；collector 为真时另有 scf-collector 的私钥。
func newFixture(t *testing.T, host string, collector bool) *fixture {
	t.Helper()
	f := &fixture{options: Options{HostID: host, ControlHostID: "control", Address: "192.0.2.2", PrivateAddress: "10.0.0.2", Collector: collector}, files: map[string][]byte{}}
	var err error
	f.caKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "MooX CA"}, IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &f.caKey.PublicKey, f.caKey)
	require.NoError(t, err)
	f.ca, err = x509.ParseCertificate(der)
	require.NoError(t, err)
	f.files[hostbundle.CAFile] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	f.options.ExpectedCA = digest(der)
	f.metadata = hostbundle.Metadata{Version: 2, HostID: host, ControlHostID: "control", BundleDir: "/data/moox/.export-1/bundles/host-bundle-1",
		CA: hostbundle.CAInfo{CertificateInfo: certificateInfo(f.ca)}, SignKeyIssued: true, SignPublicKey: PublicKey(authtest.Key("host/" + host))}
	f.files[hostbundle.HostKeyFile] = keyFile("host/" + host)
	if collector {
		f.metadata.SCFCollectorKeyIssued, f.metadata.SCFCollectorPublicKey = true, PublicKey(authtest.Key("scf"))
		f.files[hostbundle.SCFCollectorKeyFile] = keyFile("scf")
	}
	f.leafKey, err = ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	f.leaf = &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: host}, DNSNames: []string{host, "localhost"}, IPAddresses: []net.IP{net.ParseIP("192.0.2.2"), net.ParseIP("10.0.0.2"), net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	f.signLeaf(t)
	f.inventory()
	return f
}

func (f *fixture) signLeaf(t *testing.T) {
	t.Helper()
	der, err := x509.CreateCertificate(rand.Reader, f.leaf, f.ca, &f.leafKey.PublicKey, f.caKey)
	require.NoError(t, err)
	cert, err := x509.ParseCertificate(der)
	require.NoError(t, err)
	f.metadata.Certificate = certificateInfo(cert)
	f.files[hostbundle.HostCertificateFile] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	key, err := x509.MarshalPKCS8PrivateKey(f.leafKey)
	require.NoError(t, err)
	f.files[hostbundle.HostTLSKeyFile] = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
}

func (f *fixture) inventory() {
	f.metadata.Files = nil
	for name, raw := range f.files {
		f.metadata.Files = append(f.metadata.Files, hostbundle.File{Path: name, SHA256: digest(raw), Size: int64(len(raw))})
	}
	slices.SortFunc(f.metadata.Files, func(a, b hostbundle.File) int { return strings.Compare(a.Path, b.Path) })
}

func (f *fixture) download(t *testing.T) Download {
	t.Helper()
	return func(ctx context.Context, name string, out io.Writer) (int64, error) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		require.True(t, strings.HasPrefix(name, f.metadata.BundleDir+"/"))
		relative := strings.TrimPrefix(name, f.metadata.BundleDir+"/")
		raw, ok := f.files[relative]
		if relative == "bundle.json" {
			encoded, err := json.Marshal(f.metadata)
			require.NoError(t, err)
			raw, ok = append(encoded, '\n'), true
		}
		if !ok {
			return 0, os.ErrNotExist
		}
		return io.Copy(out, bytes.NewReader(raw))
	}
}

func (f *fixture) fetch(t *testing.T) (map[string][]byte, error) {
	t.Helper()
	return Fetch(context.Background(), f.download(t), f.metadata, f.options)
}

func TestFetchReturnsVerifiedHostMaterial(t *testing.T) {
	storage := newFixture(t, "storage", false)
	files, err := storage.fetch(t)
	require.NoError(t, err)
	require.Len(t, files, 4)
	require.Equal(t, keyFile("host/storage"), files[hostbundle.HostKeyFile])

	control := newFixture(t, "control", true)
	files, err = control.fetch(t)
	require.NoError(t, err)
	require.Len(t, files, 5)
	require.Equal(t, keyFile("scf"), files[hostbundle.SCFCollectorKeyFile])

	// 例行升级：目标机上的私钥原样沿用，包里只有证书三件套。
	delete(storage.files, hostbundle.HostKeyFile)
	storage.metadata.SignKeyIssued = false
	storage.inventory()
	files, err = storage.fetch(t)
	require.NoError(t, err)
	require.Len(t, files, 3)
	require.NotContains(t, files, hostbundle.HostKeyFile)
}

// 清单与期望的主机、CA、部署对不上，或带了不该有的文件，一律拒绝。
func TestFetchRejectsWrongTopologyTrustAndInventory(t *testing.T) {
	for name, mutate := range map[string]func(*fixture){
		"别的主机":                     func(f *fixture) { f.options.HostID = "overseas" },
		"别的控制机":                    func(f *fixture) { f.options.ControlHostID = "other" },
		"别的 CA":                    func(f *fixture) { f.options.ExpectedCA = strings.Repeat("b", 64) },
		"没固定 CA":                   func(f *fixture) { f.options.ExpectedCA = "" },
		"版本不对":                     func(f *fixture) { f.metadata.Version = 1 },
		"包目录是相对路径":                 func(f *fixture) { f.metadata.BundleDir = "bundles/x" },
		"公钥不合法":                    func(f *fixture) { f.metadata.SignPublicKey = "bad" },
		"声称签发了却没有私钥":               func(f *fixture) { delete(f.files, hostbundle.HostKeyFile); f.inventory() },
		"没声称签发却带了私钥":               func(f *fixture) { f.metadata.SignKeyIssued = false },
		"多带了文件":                    func(f *fixture) { f.files["admin-master.key"] = []byte("x"); f.inventory() },
		"多带了 CA 私钥":                func(f *fixture) { f.files["ca.key"] = []byte("x"); f.inventory() },
		"没部署 collector 却带了 scf 公钥": func(f *fixture) { f.metadata.SCFCollectorPublicKey = PublicKey(authtest.Key("scf")) },
		"没部署 collector 却带了 scf 私钥": func(f *fixture) {
			f.metadata.SCFCollectorPublicKey, f.metadata.SCFCollectorKeyIssued = PublicKey(authtest.Key("scf")), true
			f.files[hostbundle.SCFCollectorKeyFile] = keyFile("scf")
			f.inventory()
		},
		"部署了 collector 却没有 scf 公钥": func(f *fixture) { f.options.Collector = true },
	} {
		f := newFixture(t, "storage", false)
		mutate(f)
		_, err := f.fetch(t)
		require.Error(t, err, name)
	}
}

// 清单没问题、内容被换掉：私钥对不上登记的公钥、证书不是固定 CA 签的、证书不属于这台主机，都拒绝；报错里不带私钥内容。
func TestFetchRejectsInvalidContents(t *testing.T) {
	for name, mutate := range map[string]func(*testing.T, *fixture){
		"主机私钥对不上公钥":   func(_ *testing.T, f *fixture) { f.files[hostbundle.HostKeyFile] = keyFile("another") },
		"主机私钥不是合法种子":  func(_ *testing.T, f *fixture) { f.files[hostbundle.HostKeyFile] = []byte("not-a-key\n") },
		"scf 私钥对不上公钥": func(_ *testing.T, f *fixture) { f.files[hostbundle.SCFCollectorKeyFile] = keyFile("another") },
		"证书是给别的主机的": func(t *testing.T, f *fixture) {
			f.leaf.Subject.CommonName, f.leaf.DNSNames = "overseas", []string{"overseas"}
			f.signLeaf(t)
		},
		"证书多了地址": func(t *testing.T, f *fixture) {
			f.leaf.IPAddresses = append(f.leaf.IPAddresses, net.ParseIP("203.0.113.9"))
			f.signLeaf(t)
		},
		"直达主机的证书带了 ClientAuth": func(t *testing.T, f *fixture) {
			f.leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
			f.signLeaf(t)
		},
		"证书不是固定 CA 签的": func(t *testing.T, f *fixture) {
			other := newFixture(t, "control", true)
			f.ca, f.caKey = other.ca, other.caKey
			f.signLeaf(t)
		},
		"证书私钥对不上": func(t *testing.T, f *fixture) {
			other, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			require.NoError(t, err)
			der, err := x509.MarshalPKCS8PrivateKey(other)
			require.NoError(t, err)
			f.files[hostbundle.HostTLSKeyFile] = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
		},
		"根证书后面跟着别的内容": func(_ *testing.T, f *fixture) {
			f.files[hostbundle.CAFile] = append(f.files[hostbundle.CAFile], f.files[hostbundle.HostTLSKeyFile]...)
		},
	} {
		f := newFixture(t, "control", true)
		mutate(t, f)
		f.inventory()
		_, err := f.fetch(t)
		require.Error(t, err, name)
		require.NotContains(t, err.Error(), strings.TrimSpace(string(keyFile("host/control"))), name)
	}
	// 只能出站的主机反过来必须带 ClientAuth。
	f := newFixture(t, "factor", false)
	f.options.Outbound = true
	_, err := f.fetch(t)
	require.Error(t, err)
	f.leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}
	f.signLeaf(t)
	f.inventory()
	_, err = f.fetch(t)
	require.NoError(t, err)
}

// 传输过程：清单与 Admin 的回答不一致、文件大小或摘要对不上、超出声明的大小、中途取消，都拒绝。
func TestFetchPinsAndBoundsEveryTransfer(t *testing.T) {
	f := newFixture(t, "storage", false)
	_, err := Fetch(context.Background(), nil, f.metadata, f.options)
	require.Error(t, err)
	tamper := func(name string, change func([]byte) []byte) Download {
		inner := f.download(t)
		return func(ctx context.Context, source string, out io.Writer) (int64, error) {
			if !strings.HasSuffix(source, "/"+name) {
				return inner(ctx, source, out)
			}
			var buffer bytes.Buffer
			if _, err := inner(ctx, source, &buffer); err != nil {
				return 0, err
			}
			return io.Copy(out, bytes.NewReader(change(buffer.Bytes())))
		}
	}
	for name, download := range map[string]Download{
		"清单被换": tamper("bundle.json", func(raw []byte) []byte {
			return bytes.Replace(raw, []byte(`"host_id":"storage"`), []byte(`"host_id":"control"`), 1)
		}),
		"清单不规范": tamper("bundle.json", func(raw []byte) []byte { return append([]byte(" "), raw...) }),
		"内容被改":  tamper(hostbundle.HostCertificateFile, func(raw []byte) []byte { raw[len(raw)/2] ^= 1; return raw }),
		"内容变长":  tamper(hostbundle.HostKeyFile, func(raw []byte) []byte { return append(raw, 'x') }),
		"内容为空":  tamper(hostbundle.CAFile, func([]byte) []byte { return nil }),
	} {
		_, err := Fetch(context.Background(), download, f.metadata, f.options)
		require.Error(t, err, name)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = Fetch(canceled, f.download(t), f.metadata, f.options)
	require.ErrorIs(t, err, context.Canceled)
}
