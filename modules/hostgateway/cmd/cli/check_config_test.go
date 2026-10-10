package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
)

// writeCheckConfigFiles 在配置目录里写 MooX CA、主机网关证书（SAN 为 hostID）和调用方密钥。
func writeCheckConfigFiles(t *testing.T, configPath, hostID string) {
	t.Helper()
	dir := filepath.Dir(configPath)
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	caTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "MooX 测试 CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, KeyUsage: x509.KeyUsageCertSign, BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		t.Fatal(err)
	}
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serverDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: hostID}, DNSNames: []string{hostID},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}, caCert, &serverKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	for name, block := range map[string]*pem.Block{
		"ca.crt":     {Type: "CERTIFICATE", Bytes: caDER},
		"server.crt": {Type: "CERTIFICATE", Bytes: serverDER},
		"server.key": {Type: "EC PRIVATE KEY", Bytes: keyDER},
	} {
		if err := os.WriteFile(filepath.Join(dir, name), pem.EncodeToMemory(block), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	callerKey, err := gatewayauth.MarshalCallerKey(gatewayauth.CallerKey{Caller: "host-gateway@" + hostID, KeyID: "host-gateway@" + hostID + "-1", Secret: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "caller-host-gateway.key"), callerKey, 0o600); err != nil {
		t.Fatal(err)
	}
}

// check-config 是上线前的自检：证书、CA、调用方密钥缺失或不对时必须失败，不能等进程启动时才发现。
func TestCheckConfigVerifiesCertificatesAndKeys(t *testing.T) {
	configPath, _ := cliConfig(t)
	var output bytes.Buffer
	if code := run([]string{"check-config", "--config", configPath}, &output); code == 0 {
		t.Fatalf("证书和密钥文件都不存在时 check-config 应当失败: %s", output.String())
	}

	writeCheckConfigFiles(t, configPath, "storage")
	output.Reset()
	if code := run([]string{"check-config", "--config", configPath}, &output); code != 0 {
		t.Fatalf("文件齐全时 check-config 应当通过: %s", output.String())
	}

	// 证书的 SAN 不含本机主机 ID：和进程启动时一样被拒绝。
	writeCheckConfigFiles(t, configPath, "another-host")
	output.Reset()
	if code := run([]string{"check-config", "--config", configPath}, &output); code == 0 {
		t.Fatal("证书不包含本机主机 ID 时 check-config 应当失败")
	}
}
