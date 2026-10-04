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
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/notification"
	"trpc.group/trpc-go/trpc-go/log"
)

const certificateWatchTimerService = "trpc.moox.admin.certificates.timer"

type certificateWatchFile struct {
	Name string
	Path string
}

// certificateWatch deliberately validates only public certificate bundles.
// Private keys remain owned by the deployment scripts and are never read by
// the Admin process for this periodic check.
type certificateWatch struct {
	Files  []certificateWatchFile
	Now    func() time.Time
	Sender notification.Sender
}

func newCertificateWatchFromEnvironment() certificateWatch {
	files := make([]certificateWatchFile, 0, 3)
	for _, file := range []certificateWatchFile{
		{Name: "gateway_peer_ca", Path: os.Getenv("MOOX_GATEWAY_CA_FILE")},
		{Name: "service_gateway_ca", Path: os.Getenv("MOOX_SERVICE_GATEWAY_CA_FILE")},
		{Name: "eventbus_ca", Path: os.Getenv("MOOX_EVENTBUS_CA_FILE")},
	} {
		if strings.TrimSpace(file.Path) != "" {
			files = append(files, file)
		}
	}
	watch := certificateWatch{Files: files, Now: time.Now}
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

func (w certificateWatch) Validate(ctx context.Context) error {
	if len(w.Files) == 0 {
		log.WarnContext(ctx, "certificate watch is disabled: no public CA files are configured")
		return nil
	}
	now := time.Now()
	if w.Now != nil {
		now = w.Now()
	}
	for _, file := range w.Files {
		fingerprint, certificates, err := validatePublicCertificateBundle(file.Path, now)
		if err != nil {
			validationErr := fmt.Errorf("validate %s: %w", file.Name, err)
			w.notifyFailure(ctx, file.Name, validationErr)
			return validationErr
		}
		log.InfoContextf(ctx, "certificate_watch name=%s sha256=%s certificates=%d", file.Name, fingerprint, certificates)
	}
	return nil
}

func (w certificateWatch) notifyFailure(ctx context.Context, name string, err error) {
	log.ErrorContextf(ctx, "certificate_watch_failed name=%s error=%v", name, err)
	if w.Sender == nil {
		return
	}
	notifyCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if sendErr := w.Sender.Send(notifyCtx, notification.Message{
		Key:      "admin_certificate_watch_" + name,
		Severity: notification.SeverityCritical,
		Title:    "MooX 证书校验失败",
		Body:     fmt.Sprintf("Admin 未通过 %s 的公有证书校验：%v。SCF 发布会拒绝使用未校验的证书，请检查控制机证书文件。", name, err),
		Labels:   map[string]string{"certificate": name},
	}); sendErr != nil {
		log.ErrorContextf(ctx, "certificate_watch_notification_failed name=%s error=%v", name, sendErr)
	}
}

func validatePublicCertificateBundle(path string, now time.Time) (string, int, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", 0, fmt.Errorf("certificate file is unavailable")
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("certificate file must be a regular file")
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil || len(raw) == 0 || len(raw) > 1<<20 {
		return "", 0, fmt.Errorf("certificate file is unreadable")
	}

	rest := raw
	count := 0
	for {
		block, remaining := pem.Decode(rest)
		if block == nil {
			if strings.TrimSpace(string(rest)) != "" {
				return "", 0, fmt.Errorf("certificate file contains non-PEM data")
			}
			break
		}
		if block.Type != "CERTIFICATE" {
			return "", 0, fmt.Errorf("certificate file contains non-certificate PEM data")
		}
		certificate, parseErr := x509.ParseCertificate(block.Bytes)
		if parseErr != nil {
			return "", 0, fmt.Errorf("certificate PEM is invalid")
		}
		if now.After(certificate.NotAfter) {
			return "", 0, fmt.Errorf("certificate expired at %s", certificate.NotAfter.UTC().Format(time.RFC3339))
		}
		count++
		rest = remaining
	}
	if count == 0 {
		return "", 0, fmt.Errorf("certificate file contains no certificate")
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), count, nil
}
