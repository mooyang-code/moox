// Package pki 维护 MooX 私有 CA，并为主机网关签发服务端证书（设计文档 3.5）。
//
// CA 文件放在 control 的 secrets/pki/（权限 0600），不进 admin.db：重置 control 数据时 CA 不丢，
// 各主机的证书不必重签。CA 首次部署时生成，之后一直复用；主机证书在每次部署该主机时重新签发。
package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// CAKeyFile、CACertFile 是 CA 在 secrets/pki/ 下的文件名。
	CAKeyFile  = "ca.key"
	CACertFile = "ca.crt"
	// CAValidity 是 CA 的有效期，HostValidity 是主机网关证书的有效期（与 EventBus 一致）。
	CAValidity   = 10 * 365 * 24 * time.Hour
	HostValidity = 5 * 365 * 24 * time.Hour
	caCommonName = "MooX Private CA"
)

// EnsureCA 在 dir 下没有 CA 时生成一个；已有时校验并复用。返回是否新建和 CA 证书。
func EnsureCA(dir string, now time.Time) (bool, *x509.Certificate, error) {
	certificate, _, err := LoadCA(dir)
	if err == nil {
		return false, certificate, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return false, nil, err
	}
	if _, statErr := os.Stat(filepath.Join(dir, CAKeyFile)); statErr == nil {
		return false, nil, fmt.Errorf("%s 存在但 %s 缺失，CA 不完整，请人工检查", CAKeyFile, CACertFile)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return false, nil, fmt.Errorf("生成 CA 私钥: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return false, nil, err
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: caCommonName, Organization: []string{"MooX"}},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(CAValidity),
		IsCA: true, BasicConstraintsValid: true, MaxPathLenZero: true,
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return false, nil, fmt.Errorf("签发 CA 证书: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return false, nil, err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, nil, fmt.Errorf("创建 %s: %w", dir, err)
	}
	// 先写证书、后写私钥：中途失败时只剩证书、没有私钥，下一次 EnsureCA 会自动重建；反过来会留下"有私钥无证书"，
	// 需要人工介入。
	if err := WriteSecretFile(filepath.Join(dir, CACertFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})); err != nil {
		return false, nil, err
	}
	if err := WriteSecretFile(filepath.Join(dir, CAKeyFile), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})); err != nil {
		return false, nil, err
	}
	certificate, err = x509.ParseCertificate(der)
	return true, certificate, err
}

// LoadCA 读取 CA 证书和私钥。
func LoadCA(dir string) (*x509.Certificate, crypto.Signer, error) {
	certPEM, err := os.ReadFile(filepath.Join(dir, CACertFile))
	if err != nil {
		return nil, nil, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(dir, CAKeyFile))
	if err != nil {
		return nil, nil, err
	}
	certificate, err := ParseCertificatePEM(certPEM)
	if err != nil {
		return nil, nil, fmt.Errorf("解析 CA 证书: %w", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, nil, errors.New("CA 私钥不是 PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("解析 CA 私钥: %w", err)
	}
	signer, ok := parsed.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("CA 私钥不能用于签名")
	}
	if !certificate.IsCA {
		return nil, nil, errors.New("CA 证书没有 CA 标记")
	}
	return certificate, signer, nil
}

// HostCertificate 是签发给一台主机网关的证书。
type HostCertificate struct {
	CertPEM []byte
	KeyPEM  []byte
	Cert    *x509.Certificate
}

// IssueHostCertificate 为主机网关签发服务端证书；SAN 包含主机 ID 和主机的全部地址。
func IssueHostCertificate(dir, hostID string, addresses []string, now time.Time) (HostCertificate, error) {
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		return HostCertificate{}, errors.New("主机 ID 不能为空")
	}
	ca, signer, err := LoadCA(dir)
	if err != nil {
		return HostCertificate{}, fmt.Errorf("读取 MooX 私有 CA: %w", err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return HostCertificate{}, fmt.Errorf("生成主机网关私钥: %w", err)
	}
	serial, err := newSerial()
	if err != nil {
		return HostCertificate{}, err
	}
	notAfter := now.Add(HostValidity)
	if notAfter.After(ca.NotAfter) {
		notAfter = ca.NotAfter
	}
	template := &x509.Certificate{
		SerialNumber: serial, Subject: pkix.Name{CommonName: hostID, Organization: []string{"MooX"}},
		NotBefore: now.Add(-time.Hour), NotAfter: notAfter,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames: []string{hostID},
	}
	for _, address := range addresses {
		address = strings.TrimSpace(address)
		if address == "" {
			continue
		}
		if ip := net.ParseIP(address); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else if address != hostID {
			template.DNSNames = append(template.DNSNames, address)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, signer)
	if err != nil {
		return HostCertificate{}, fmt.Errorf("签发主机网关证书: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return HostCertificate{}, err
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return HostCertificate{}, err
	}
	return HostCertificate{
		CertPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		KeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}),
		Cert:    certificate,
	}, nil
}

// ParseCertificatePEM 解析 PEM 中的第一张证书。
func ParseCertificatePEM(raw []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("不是 PEM 证书")
	}
	return x509.ParseCertificate(block.Bytes)
}

// WriteSecretFile 以 0600 原子写入文件。
func WriteSecretFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建 %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func newSerial() (*big.Int, error) {
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 126))
	if err != nil {
		return nil, fmt.Errorf("生成证书序列号: %w", err)
	}
	return serial, nil
}
