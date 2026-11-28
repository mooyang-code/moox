package webserver

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/mooyang-code/moox/modules/admin/internal/privatefiles"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// certificateLifetime 是网页证书的有效期。苹果系统要求自签的服务端证书不超过 825 天。
	certificateLifetime = 365 * 24 * time.Hour
	// renewBefore 是剩余不足多少时重签。
	renewBefore = 30 * 24 * time.Hour

	certFile = "server.crt"
	keyFile  = "server.key"
)

// certificates 管 9527 的网页证书：用 MooX CA 签，存在 Admin 数据目录的 web-cert/，内存里的当前证书供 TLS 握手读取。
// 不动主机证书，也不换 CA。
type certificates struct {
	address, pkiDir, dir string
	now                  func() time.Time

	mu      sync.Mutex
	current atomic.Pointer[tls.Certificate]
}

func (c *certificates) get(*tls.ClientHelloInfo) (*tls.Certificate, error) {
	if certificate := c.current.Load(); certificate != nil {
		return certificate, nil
	}
	return nil, errors.New("网页证书还没有准备好")
}

// Refresh 在启动时和每 10 分钟随证书巡检执行（幂等）：手里的证书由当前 CA 签、SAN 是当前地址、剩余不少于 30 天就直接返回、无副作用；
// 否则重签、写入 web-cert/ 并热替换，之后的新连接拿到新证书。
func (c *certificates) Refresh() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	store, err := pki.Open(c.pkiDir)
	if err != nil {
		return fmt.Errorf("打开 MooX CA 目录失败：%w", err)
	}
	defer store.Close()
	_, ca, err := store.CACertificate()
	if err != nil {
		return fmt.Errorf("读取 MooX CA 失败：%w", err)
	}
	now := c.now()
	if current := c.current.Load(); current != nil && c.usable(current.Leaf, ca, now) {
		return nil
	}
	dir, err := privatefiles.OpenRoot(c.dir)
	if err != nil {
		return fmt.Errorf("打开网页证书目录失败：%w", err)
	}
	defer dir.Close()
	if stored, err := c.load(dir); err == nil && c.usable(stored.Leaf, ca, now) {
		c.current.Store(stored)
		return nil
	}
	certPEM, keyPEM, err := store.IssueWebCertificate(c.address, certificateLifetime)
	if err != nil {
		return fmt.Errorf("签发网页证书失败：%w", err)
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return err
	}
	// 先写私钥再写证书；中途断了下次启动两者对不上，会重签。
	if err := privatefiles.Write(dir, keyFile, keyPEM); err != nil {
		return err
	}
	if err := privatefiles.Write(dir, certFile, certPEM); err != nil {
		return err
	}
	c.current.Store(&pair)
	log.Infof("[web] 已签发网页证书：地址 %s，到期 %s", c.address, pair.Leaf.NotAfter.UTC().Format(time.RFC3339))
	return nil
}

func (c *certificates) load(dir *os.Root) (*tls.Certificate, error) {
	certPEM, err := privatefiles.ReadAt(dir, certFile, 64<<10)
	if err != nil {
		return nil, err
	}
	keyPEM, err := privatefiles.ReadAt(dir, keyFile, 64<<10)
	if err != nil {
		return nil, err
	}
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, err
	}
	return &pair, nil
}

// usable 判断证书能否继续用：由当前 CA 签（全新部署换了 CA 就重签）、SAN 是当前地址、已生效且剩余不少于 30 天。
func (c *certificates) usable(leaf, ca *x509.Certificate, now time.Time) bool {
	return leaf != nil && leaf.CheckSignatureFrom(ca) == nil && leaf.VerifyHostname(c.address) == nil &&
		!now.Before(leaf.NotBefore) && leaf.NotAfter.Sub(now) >= renewBefore
}
