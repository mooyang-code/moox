// Package identitybundle 校验并取回控制机签发的主机身份包：主机签名私钥（新签发时）、主机证书、根证书，
// 控制机另有 scf-collector 的私钥。它不生成信任根，也不读操作电脑的配置文件。
package identitybundle

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"io/fs"
	"net"
	"path"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/mooyang-code/moox/packages/servicecatalog/hostbundle"
)

const maxMetadata = 128 << 10

// Options 是校验一台主机身份包用的期望值。
type Options struct {
	HostID, ControlHostID, Address, PrivateAddress string
	// Outbound 表示目标是只能出站的主机：网关证书必须同时带 ClientAuth，用来与 control 建立反向连接。
	Outbound bool
	// Collector 表示这台主机部署了 collector：身份包要带 scf-collector 的公钥（私钥只在新签发时有）。
	Collector bool
	// ExpectedCA 是固定的 MooX CA 指纹（操作电脑第一次部署控制机时记下的）。
	ExpectedCA string
}

func digest(raw []byte) string { hash := sha256.Sum256(raw); return hex.EncodeToString(hash[:]) }
func validHash(value string) bool {
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 32 && value == strings.ToLower(value)
}
func canonical(raw []byte, out any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(out) != nil || decoder.Decode(new(any)) != io.EOF {
		return errors.New("host material requires one JSON document with known fields")
	}
	encoded, err := json.Marshal(out)
	if err != nil || !bytes.Equal(raw, append(encoded, '\n')) {
		return errors.New("host material requires canonical JSON encoding")
	}
	return nil
}
func privatePath(name string) bool {
	return fs.ValidPath(name) && name != "." && len(name) <= 512 && !strings.ContainsAny(name, "\\\x00\r\n")
}

// wantedFiles 是清单应当列出的文件（排序后）：证书三件套，加这次新签发的私钥。
func wantedFiles(metadata hostbundle.Metadata) []string {
	files := []string{hostbundle.HostCertificateFile, hostbundle.HostTLSKeyFile, hostbundle.CAFile}
	if metadata.SignKeyIssued {
		files = append(files, hostbundle.HostKeyFile)
	}
	if metadata.SCFCollectorKeyIssued {
		files = append(files, hostbundle.SCFCollectorKeyFile)
	}
	slices.Sort(files)
	return files
}

func validateMetadata(metadata hostbundle.Metadata, options Options) error {
	if !servicecatalog.ValidHostID(options.HostID) || !servicecatalog.ValidHostID(options.ControlHostID) || !servicecatalog.ValidHostAddress(options.Address) ||
		options.PrivateAddress != "" && !servicecatalog.ValidHostAddress(options.PrivateAddress) || !validHash(options.ExpectedCA) {
		return errors.New("host material requires canonical topology and a pinned CA")
	}
	if metadata.Version != 2 || metadata.HostID != options.HostID || metadata.ControlHostID != options.ControlHostID || metadata.CA.SHA256 != options.ExpectedCA ||
		!path.IsAbs(metadata.BundleDir) || metadata.BundleDir == "/" || len(metadata.BundleDir) > 4096 || path.Clean(metadata.BundleDir) != metadata.BundleDir || strings.ContainsAny(metadata.BundleDir, "\\\x00\r\n") {
		return errors.New("host material metadata does not match its expected topology and identity")
	}
	if _, err := gatewayauth.ParsePublicKey(metadata.SignPublicKey); err != nil {
		return errors.New("host material has no valid host signing public key")
	}
	// scf-collector 的公钥只属于部署了 collector 的主机；别的主机的身份包带它就是越界。
	_, scfErr := gatewayauth.ParsePublicKey(metadata.SCFCollectorPublicKey)
	if options.Collector != (metadata.SCFCollectorPublicKey != "") || options.Collector && scfErr != nil || metadata.SCFCollectorKeyIssued && !options.Collector {
		return errors.New("host material scf-collector identity does not match the deployment")
	}
	wanted := wantedFiles(metadata)
	if len(metadata.Files) != len(wanted) {
		return errors.New("host material inventory contains missing or unrelated files")
	}
	var total int64
	for i, file := range metadata.Files {
		total += file.Size
		if file.Path != wanted[i] || !privatePath(file.Path) || !validHash(file.SHA256) || file.Size <= 0 || file.Size > 1<<20 || total > 16<<20 {
			return errors.New("host material inventory contains missing or unrelated files")
		}
	}
	return nil
}

// Download 由已校验主机密钥的 SSH 连接实现。
type Download func(context.Context, string, io.Writer) (int64, error)

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int64
	overflow bool
}

func (b *boundedBuffer) Write(raw []byte) (int, error) {
	if int64(len(raw)) > b.limit-int64(b.buffer.Len()) {
		b.overflow = true
		return 0, errors.New("host material download exceeded declared size")
	}
	return b.buffer.Write(raw)
}

func downloadFile(ctx context.Context, download Download, source string, max int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	buffer := &boundedBuffer{limit: max}
	n, err := download(ctx, source, buffer)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err != nil || buffer.overflow || n != int64(buffer.buffer.Len()) || n == 0 {
		return nil, errors.New("host material download failed or returned an invalid byte count")
	}
	return buffer.buffer.Bytes(), nil
}

// Fetch 按 Admin 返回的清单把身份包取回内存并逐项校验：清单与期望的主机、CA 一致，每个文件的大小与摘要对得上，
// 私钥对应清单里的公钥，证书在固定的 CA 下验得过。返回文件名 → 内容。
func Fetch(ctx context.Context, download Download, source hostbundle.Metadata, options Options) (map[string][]byte, error) {
	if download == nil {
		return nil, errors.New("host material requires a verified SSH downloader")
	}
	if err := validateMetadata(source, options); err != nil {
		return nil, err
	}
	raw, err := downloadFile(ctx, download, path.Join(source.BundleDir, "bundle.json"), maxMetadata)
	if err != nil {
		return nil, err
	}
	var metadata hostbundle.Metadata
	if err := canonical(raw, &metadata); err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(metadata, source) {
		return nil, errors.New("downloaded host material differs from the Admin response")
	}
	files := map[string][]byte{}
	for _, item := range source.Files {
		content, err := downloadFile(ctx, download, path.Join(source.BundleDir, item.Path), item.Size)
		if err != nil {
			return nil, err
		}
		if int64(len(content)) != item.Size || digest(content) != item.SHA256 {
			return nil, errors.New("downloaded host material digest or length differs from its inventory")
		}
		files[item.Path] = content
	}
	for name, public := range map[string]string{hostbundle.HostKeyFile: source.SignPublicKey, hostbundle.SCFCollectorKeyFile: source.SCFCollectorPublicKey} {
		content, issued := files[name]
		if !issued {
			continue
		}
		key, err := gatewayauth.ParseSigningKey(string(content))
		if err != nil || gatewayauth.EncodePublicKey(gatewayauth.Credentials{Key: key}.PublicKey()) != public {
			return nil, errors.New("host material signing key does not match its registered public key")
		}
	}
	if err := verifyCertificates(source, files, options); err != nil {
		return nil, err
	}
	return files, nil
}

func onePEM(raw []byte, kind string) ([]byte, error) {
	// 前后不能有多余内容，也不能有第二个 PEM 块：根证书后面不能跟着私钥。
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != kind || len(block.Headers) != 0 || !bytes.Equal(bytes.TrimSpace(raw), bytes.TrimSpace(pem.EncodeToMemory(block))) || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("host material requires a single PEM block of the expected type")
	}
	return block.Bytes, nil
}

func certificate(raw []byte, info hostbundle.CertificateInfo) (*x509.Certificate, error) {
	der, err := onePEM(raw, "CERTIFICATE")
	if err != nil {
		return nil, err
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil || digest(der) != info.SHA256 || cert.SerialNumber.Text(16) != info.Serial || !cert.NotAfter.Equal(info.NotAfter) {
		return nil, errors.New("host material certificate differs from its public metadata")
	}
	return cert, nil
}

func verifyCertificates(metadata hostbundle.Metadata, files map[string][]byte, options Options) error {
	ca, err := certificate(files[hostbundle.CAFile], metadata.CA.CertificateInfo)
	if err != nil {
		return err
	}
	now := time.Now()
	if !ca.IsCA || !ca.BasicConstraintsValid || ca.KeyUsage&x509.KeyUsageCertSign == 0 || now.Before(ca.NotBefore) || !now.Before(ca.NotAfter) || ca.CheckSignatureFrom(ca) != nil {
		return errors.New("host material CA is not a valid current self-signed trust root")
	}
	leaf, err := certificate(files[hostbundle.HostCertificateFile], metadata.Certificate)
	if err != nil {
		return err
	}
	if leaf.IsCA || leaf.Subject.CommonName != options.HostID || len(leaf.EmailAddresses) != 0 || len(leaf.URIs) != 0 {
		return errors.New("host material leaf certificate does not identify its target host")
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: options.HostID, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return errors.New("host material leaf certificate does not verify under the pinned CA")
	}
	// 只有出站主机的网关证书带 ClientAuth；直达主机的证书不能用于反向连接。
	usages := []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	if options.Outbound {
		usages = append(usages, x509.ExtKeyUsageClientAuth)
	}
	if !slices.Equal(leaf.ExtKeyUsage, usages) || len(leaf.UnknownExtKeyUsage) != 0 {
		return errors.New("host material leaf certificate key usage does not match the host reachability")
	}
	// 回环地址每张主机证书都有：EventBus 用所在主机的主机证书，同机组件经 127.0.0.1 连它。
	names, ips := []string{options.HostID, "localhost"}, []string{"127.0.0.1", "::1"}
	for _, address := range []string{options.Address, options.PrivateAddress} {
		if address == "" {
			continue
		}
		if ip := net.ParseIP(address); ip != nil {
			ips = append(ips, ip.String())
		} else {
			names = append(names, strings.ToLower(address))
		}
	}
	slices.Sort(names)
	names = slices.Compact(names)
	slices.Sort(ips)
	ips = slices.Compact(ips)
	actualNames := slices.Clone(leaf.DNSNames)
	actualIPs := make([]string, 0, len(leaf.IPAddresses))
	for _, ip := range leaf.IPAddresses {
		actualIPs = append(actualIPs, ip.String())
	}
	slices.Sort(actualNames)
	slices.Sort(actualIPs)
	if !slices.Equal(names, actualNames) || !slices.Equal(ips, actualIPs) {
		return errors.New("host material certificate SAN differs from target public/private addresses")
	}
	key, err := onePEM(files[hostbundle.HostTLSKeyFile], "PRIVATE KEY")
	if err != nil {
		return err
	}
	if _, err := x509.ParsePKCS8PrivateKey(key); err != nil {
		return errors.New("host material leaf private key is invalid")
	}
	if _, err := tls.X509KeyPair(files[hostbundle.HostCertificateFile], files[hostbundle.HostTLSKeyFile]); err != nil {
		return errors.New("host material leaf private key does not match its certificate")
	}
	return nil
}
