// Package tlsconfig 为主机网关的跨主机入口加载服务端证书。证书由 MooX 私有 CA 签发，
// 部署这台主机时重新签发；调用方只信任 MooX 私有 CA。
package tlsconfig

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"time"
)

// Server 是跨主机入口的 TLS 配置与证书信息。
type Server struct {
	Config   *tls.Config
	NotAfter time.Time
}

// LoadServer 读取服务端证书和私钥，并校验证书由 caFile 中的 MooX 私有 CA 签发、包含本机主机 ID。
func LoadServer(certFile, keyFile, caFile, hostID string, now time.Time) (Server, error) {
	pair, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return Server{}, fmt.Errorf("读取主机网关证书: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return Server{}, errors.New("主机网关证书为空")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return Server{}, fmt.Errorf("解析主机网关证书: %w", err)
	}
	roots, err := LoadCAPool(caFile)
	if err != nil {
		return Server{}, err
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, DNSName: hostID, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}); err != nil {
		return Server{}, fmt.Errorf("主机网关证书没有通过 MooX 私有 CA 校验，或不包含主机 %s: %w", hostID, err)
	}
	pair.Leaf = leaf
	return Server{
		Config:   &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: tls.VersionTLS12},
		NotAfter: leaf.NotAfter,
	}, nil
}

// LoadCAPool 读取 MooX 私有 CA 证书。
func LoadCAPool(caFile string) (*x509.CertPool, error) {
	raw, err := os.ReadFile(caFile)
	if err != nil {
		return nil, fmt.Errorf("读取 MooX 私有 CA: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, errors.New("MooX 私有 CA 文件中没有证书")
	}
	return pool, nil
}

// Listen 在 address 上监听 TLS。
func Listen(address string, config *tls.Config) (net.Listener, error) {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, err
	}
	return tls.NewListener(listener, config), nil
}
