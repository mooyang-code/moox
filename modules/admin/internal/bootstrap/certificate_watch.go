package bootstrap

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/service/placement"
	"github.com/mooyang-code/moox/packages/notification"
	"trpc.group/trpc-go/trpc-go/log"
)

const certificateWatchTimerService = "trpc.moox.admin.certificates.timer"

// certificateExpiryWarning 是证书到期前开始告警的提前量（设计文档 3.5）。
const certificateExpiryWarning = 90 * 24 * time.Hour

type certificateWatchFile struct {
	Name string
	Path string
}

// certificateWatch 巡检 MooX 私有 CA、EventBus CA 和各主机网关的服务端证书，到期前 90 天告警。
// 只读公开证书：私钥由部署脚本管理，Admin 进程从不读取。主机网关的证书到期时间来自心跳上报。
type certificateWatch struct {
	Files  []certificateWatchFile
	Hosts  func(context.Context) (map[string]time.Time, error)
	Now    func() time.Time
	Sender notification.Sender
}

func newCertificateWatchFromEnvironment(placements *placement.Service) certificateWatch {
	files := make([]certificateWatchFile, 0, 2)
	for _, file := range []certificateWatchFile{
		{Name: "moox_ca", Path: os.Getenv("MOOX_PKI_CA_FILE")},
		{Name: "eventbus_ca", Path: os.Getenv("MOOX_EVENTBUS_CA_FILE")},
	} {
		if strings.TrimSpace(file.Path) != "" {
			files = append(files, file)
		}
	}
	watch := certificateWatch{Files: files, Now: time.Now}
	if placements != nil {
		watch.Hosts = func(ctx context.Context) (map[string]time.Time, error) {
			statuses, err := placements.ListGatewayStatus(ctx)
			if err != nil {
				return nil, err
			}
			out := make(map[string]time.Time, len(statuses))
			for host, status := range statuses {
				if status.CertificateNotAfter != nil {
					out[host] = *status.CertificateNotAfter
				}
			}
			return out, nil
		}
	}
	if webhookURL := strings.TrimSpace(os.Getenv("MOOX_NOTIFICATION_WEBHOOK_URL")); webhookURL != "" {
		channelType := strings.TrimSpace(os.Getenv("MOOX_NOTIFICATION_CHANNEL_TYPE"))
		if channelType == "" {
			channelType = string(notification.ChannelTypeWeCom)
		}
		sender, err := notification.NewSender(notification.ChannelConfig{Type: notification.ChannelType(channelType), WebhookURL: webhookURL})
		if err != nil {
			log.Warnf("certificate watch notification disabled: %v", err)
		} else {
			watch.Sender = sender
		}
	}
	return watch
}

// Validate 校验本机的公开证书文件，失效时返回错误；临近到期和主机网关证书的问题只告警。
func (w certificateWatch) Validate(ctx context.Context) error {
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	if len(w.Files) == 0 {
		log.WarnContext(ctx, "certificate watch: no public CA files are configured")
	}
	for _, file := range w.Files {
		fingerprint, certificates, notAfter, err := validatePublicCertificateBundle(file.Path, now)
		if err != nil {
			validationErr := fmt.Errorf("validate %s: %w", file.Name, err)
			w.notify(ctx, file.Name, notification.SeverityCritical, "MooX 证书校验失败",
				fmt.Sprintf("Admin 未通过 %s 的公开证书校验：%v，请检查 control 上的证书文件。", file.Name, validationErr))
			return validationErr
		}
		log.InfoContextf(ctx, "certificate_watch name=%s sha256=%s certificates=%d not_after=%s", file.Name, fingerprint, certificates, notAfter.UTC().Format(time.RFC3339))
		w.warnIfExpiring(ctx, file.Name, notAfter, now)
	}
	if w.Hosts == nil {
		return nil
	}
	hosts, err := w.Hosts(ctx)
	if err != nil {
		log.WarnContextf(ctx, "certificate watch: read host gateway certificates: %v", err)
		return nil
	}
	ids := make([]string, 0, len(hosts))
	for host := range hosts {
		ids = append(ids, host)
	}
	sort.Strings(ids)
	for _, host := range ids {
		notAfter := hosts[host]
		name := "host_gateway_" + host
		if now.After(notAfter) {
			w.notify(ctx, name, notification.SeverityCritical, "主机网关证书已过期",
				fmt.Sprintf("主机 %s 的主机网关证书已于 %s 过期，跨主机调用会失败。重新部署这台主机即可换发。", host, notAfter.UTC().Format(time.RFC3339)))
			continue
		}
		w.warnIfExpiring(ctx, name, notAfter, now)
	}
	return nil
}

func (w certificateWatch) warnIfExpiring(ctx context.Context, name string, notAfter, now time.Time) {
	if notAfter.Sub(now) > certificateExpiryWarning {
		return
	}
	w.notify(ctx, name, notification.SeverityWarning, "MooX 证书即将到期",
		fmt.Sprintf("%s 将于 %s 到期（剩余 %d 天）。主机网关证书在重新部署该主机时自动换发。", name,
			notAfter.UTC().Format(time.RFC3339), int(notAfter.Sub(now).Hours()/24)))
}

func (w certificateWatch) notify(ctx context.Context, name string, severity notification.Severity, title, body string) {
	log.ErrorContextf(ctx, "certificate_watch_alert name=%s severity=%s %s", name, severity, body)
	if w.Sender == nil {
		return
	}
	notifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if sendErr := w.Sender.Send(notifyCtx, notification.Message{
		Key: "admin_certificate_watch_" + name, Severity: severity, Title: title, Body: body,
		Labels: map[string]string{"certificate": name},
	}); sendErr != nil {
		log.ErrorContextf(ctx, "certificate_watch_notification_failed name=%s error=%v", name, sendErr)
	}
}

// validatePublicCertificateBundle 校验 PEM 证书文件，返回指纹、证书数量和最早的到期时间。
func validatePublicCertificateBundle(path string, now time.Time) (string, int, time.Time, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", 0, time.Time{}, fmt.Errorf("certificate file is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", 0, time.Time{}, fmt.Errorf("certificate file must be a regular file")
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return "", 0, time.Time{}, fmt.Errorf("certificate file is unreadable")
	}

	rest := raw
	count := 0
	var earliest time.Time
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			if strings.TrimSpace(string(rest)) != "" {
				return "", 0, time.Time{}, fmt.Errorf("certificate file contains non-PEM data")
			}
			break
		}
		if block.Type != "CERTIFICATE" {
			return "", 0, time.Time{}, fmt.Errorf("certificate file contains non-certificate PEM data")
		}
		certificate, parseErr := x509.ParseCertificate(block.Bytes)
		if parseErr != nil {
			return "", 0, time.Time{}, fmt.Errorf("certificate PEM is invalid")
		}
		if now.After(certificate.NotAfter) {
			return "", 0, time.Time{}, fmt.Errorf("certificate expired at %s", certificate.NotAfter.UTC().Format(time.RFC3339))
		}
		if earliest.IsZero() || certificate.NotAfter.Before(earliest) {
			earliest = certificate.NotAfter
		}
		count++
		rest = remaining
	}
	if count == 0 {
		return "", 0, time.Time{}, fmt.Errorf("certificate file contains no certificate")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), count, earliest, nil
}
