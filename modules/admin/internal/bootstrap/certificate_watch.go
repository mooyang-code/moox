package bootstrap

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/mooyang-code/moox/modules/admin/internal/service/sysdeploy"
	"github.com/mooyang-code/moox/packages/hostpolicy"
	"github.com/mooyang-code/moox/packages/notification"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	certificateWatchTimerService = "trpc.moox.admin.certificates.timer"
	certificateWatchTimeout      = 30 * time.Second
	certificateExpiryWarning     = 90 * 24 * time.Hour
)

type certificateWatchFile struct {
	Name string
	Path string
}

// Only fixed public certificate paths are read. The watcher never initializes
// PKI, reads private keys, signs certificates or repairs a missing trust root.
type certificateWatch struct {
	PKIDir string
	Hosts  func(context.Context) ([]pki.HostIdentity, error)
	Files  []certificateWatchFile
	Now    func() time.Time
	// Senders 每次巡检时取一次告警渠道；为空或返回空时只记录不推送。
	Senders func() []notification.Sender
}

func newCertificateWatchFromEnvironment(db *gorm.DB) certificateWatch {
	dir := strings.TrimSpace(os.Getenv("MOOX_ADMIN_PKI_DIR"))
	if dir == "" {
		if master := strings.TrimSpace(os.Getenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE")); master != "" {
			dir = filepath.Join(filepath.Dir(master), "pki")
		} else {
			dir = filepath.Join("..", "secrets", "pki")
		}
	}
	watch := certificateWatch{PKIDir: dir, Now: time.Now, Hosts: func(ctx context.Context) ([]pki.HostIdentity, error) {
		if db == nil {
			return nil, errors.New("host certificate inventory requires the Admin database")
		}
		var rows []sysdeploy.HostRecord
		if err := db.WithContext(ctx).Select("c_host_id", "c_address", "c_private_address").Order("c_host_id").Limit(1025).Find(&rows).Error; err != nil {
			return nil, errors.New("registered host certificate inventory is unavailable")
		}
		hosts := make([]pki.HostIdentity, 0, len(rows))
		for _, row := range rows {
			hosts = append(hosts, pki.HostIdentity{HostID: row.HostID, Address: row.Address, PrivateAddress: row.PrivateAddress})
		}
		return hosts, nil
	}}
	if path := strings.TrimSpace(os.Getenv("MOOX_EVENTBUS_CA_FILE")); path != "" {
		watch.Files = []certificateWatchFile{{Name: "eventbus_ca", Path: path}}
	}
	if path := strings.TrimSpace(os.Getenv("MOOX_HOST_POLICY_FILE")); path != "" {
		watch.Senders = func() []notification.Sender { return hostPolicySenders(path) }
	}
	return watch
}

// hostPolicySenders 从本机主机策略缓存（HostAgent 从 Monitor 同步）读取全部告警渠道。
// 每次巡检重新读取，用户在 Monitor 修改渠道后无需重启 Admin；读取失败只记录。
func hostPolicySenders(path string) []notification.Sender {
	policy, err := hostpolicy.Read(path)
	if err != nil {
		log.Warn("certificate watch cannot read the host policy cache; findings are only logged")
		return nil
	}
	var senders []notification.Sender
	for _, channel := range policy.NotificationChannels {
		sender, err := notification.NewSender(notification.ChannelConfig{Type: notification.ChannelType(channel.Type), WebhookURL: channel.WebhookURL})
		if err != nil {
			log.Warn("certificate watch skipped an invalid notification channel")
			continue
		}
		senders = append(senders, sender)
	}
	return senders
}

type certificateFinding struct {
	name, detail string
}

func (w certificateWatch) Validate(ctx context.Context) error {
	now := time.Now().UTC()
	if w.Now != nil {
		now = w.Now().UTC()
	}
	var failures, warnings []certificateFinding
	fail := func(name string, err error) { failures = append(failures, certificateFinding{name, err.Error()}) }
	inspect := func(name string, certificates []*x509.Certificate) error {
		if err := validateCertificateTimes(certificates, now); err != nil {
			fail(name, err)
			return err
		}
		for _, cert := range certificates {
			log.InfoContextf(ctx, "certificate_watch name=%s sha256=%x not_after=%s", name, sha256.Sum256(cert.Raw), cert.NotAfter.UTC().Format(time.RFC3339))
			if !cert.NotAfter.After(now.Add(certificateExpiryWarning)) {
				warnings = append(warnings, certificateFinding{name, "expires at " + cert.NotAfter.UTC().Format(time.RFC3339)})
			}
		}
		return nil
	}
	root, err := openPublicPKIRoot(w.PKIDir)
	if err != nil {
		fail("moox_ca", err)
	} else {
		defer root.Close()
		certificates, err := readPublicCertificateBundle(root, "ca.crt")
		var ca *x509.Certificate
		if err != nil {
			fail("moox_ca", err)
		} else if len(certificates) != 1 || !certificates[0].IsCA || !certificates[0].BasicConstraintsValid || certificates[0].CheckSignatureFrom(certificates[0]) != nil {
			fail("moox_ca", errors.New("MooX root must be one valid self-signed CA certificate"))
		} else if inspect("moox_ca", certificates) == nil {
			ca = certificates[0]
		}
		var hosts []pki.HostIdentity
		if w.Hosts == nil {
			fail("host_inventory", errors.New("registered host certificate inventory is not configured"))
		} else if hosts, err = w.Hosts(ctx); err != nil {
			fail("host_inventory", err)
		} else if len(hosts) == 0 || len(hosts) > 1024 {
			fail("host_inventory", errors.New("registered host certificate inventory requires 1..1024 hosts"))
		} else {
			seen := map[string]bool{}
			for _, host := range hosts {
				if ctx.Err() != nil {
					fail("host_inventory", ctx.Err())
					break
				}
				if !servicecatalog.ValidHostID(host.HostID) || seen[host.HostID] || !servicecatalog.ValidHostAddress(host.Address) || host.PrivateAddress != "" && !servicecatalog.ValidHostAddress(host.PrivateAddress) {
					fail("host_inventory", errors.New("registered host identity is invalid or duplicated"))
					continue
				}
				seen[host.HostID] = true
				name := "host_gateway@" + host.HostID
				certificates, err := readPublicCertificateBundle(root, "hosts/"+host.HostID+".crt")
				if err != nil {
					fail(name, err)
					continue
				}
				if len(certificates) != 1 || certificates[0].IsCA {
					fail(name, errors.New("host inventory must contain exactly one server certificate"))
					continue
				}
				if inspect(name, certificates) != nil || ca == nil {
					continue
				}
				roots := x509.NewCertPool()
				roots.AddCert(ca)
				for _, identity := range []string{host.HostID, host.Address, host.PrivateAddress} {
					if identity == "" {
						continue
					}
					if _, err := certificates[0].Verify(x509.VerifyOptions{Roots: roots, DNSName: identity, CurrentTime: now}); err != nil {
						fail(name, errors.New("host certificate does not match the MooX CA, server usage or registered SANs"))
						break
					}
				}
			}
		}
	}
	for _, file := range w.Files {
		parent, err := os.OpenRoot(filepath.Dir(file.Path))
		if err != nil {
			fail(file.Name, errors.New("public certificate directory is unavailable"))
			continue
		}
		certificates, err := readPublicCertificateBundle(parent, filepath.Base(file.Path))
		parent.Close()
		if err != nil {
			fail(file.Name, err)
		} else {
			inspect(file.Name, certificates)
		}
	}
	w.notify(ctx, notification.SeverityCritical, failures)
	w.notify(ctx, notification.SeverityWarning, warnings)
	if len(failures) != 0 {
		return fmt.Errorf("certificate watch found %d invalid or unavailable public certificates/inventories", len(failures))
	}
	log.InfoContextf(ctx, "certificate_watch completed expiry_warnings=%d", len(warnings))
	return ctx.Err()
}

func (w certificateWatch) notify(ctx context.Context, severity notification.Severity, findings []certificateFinding) {
	if len(findings) == 0 {
		return
	}
	var body strings.Builder
	fmt.Fprintf(&body, "证书巡检发现 %d 项%s：", len(findings), severity)
	for i, finding := range findings {
		log.WarnContextf(ctx, "certificate_watch severity=%s name=%s detail=%s", severity, finding.name, finding.detail)
		if i < 12 {
			fmt.Fprintf(&body, "\n%s: %s", finding.name, finding.detail)
		}
	}
	if len(findings) > 12 {
		body.WriteString("\n其余项目见 Admin 巡检日志。")
	}
	if w.Senders == nil {
		return
	}
	senders := w.Senders()
	if len(senders) == 0 {
		return
	}
	title := "MooX 证书将在 90 天内到期"
	if severity == notification.SeverityCritical {
		title = "MooX 证书巡检失败"
	}
	notifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	message := notification.Message{Key: "admin_certificate_watch_" + string(severity), Severity: severity, Title: title, Body: body.String(), Labels: map[string]string{"source": "admin_certificate_watch"}}
	for _, sender := range senders {
		if err := sender.Send(notifyCtx, message); err != nil {
			log.ErrorContext(ctx, "certificate_watch notification failed")
		}
	}
}
