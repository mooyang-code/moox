package bootstrap

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/mooyang-code/moox/modules/admin/internal/pki"
	"github.com/mooyang-code/moox/packages/notification"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/server"
)

type certificateNotifications struct {
	messages []notification.Message
}

func (s *certificateNotifications) Send(ctx context.Context, message notification.Message) error {
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("notification must have a deadline")
	}
	s.messages = append(s.messages, message)
	return nil
}

func certificateWatchFixture(t *testing.T, caLifetime, hostLifetime time.Duration) (certificateWatch, *certificateNotifications) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "MooX Fixture CA"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(caLifetime), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	require.NoError(t, err)
	dir := filepath.Join(t.TempDir(), "pki")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "hosts"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ca.crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0o600))
	hosts := []pki.HostIdentity{
		{HostID: "control", Address: "192.0.2.1", PrivateAddress: "control.internal.example.test"},
		{HostID: "storage", Address: "2001:db8::2"},
	}
	for i, host := range hosts {
		leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(i + 2)), Subject: pkix.Name{CommonName: host.HostID}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(hostLifetime), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{host.HostID}, IPAddresses: []net.IP{net.ParseIP(host.Address)}}
		if host.PrivateAddress != "" {
			leaf.DNSNames = append(leaf.DNSNames, host.PrivateAddress)
		}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, key)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(filepath.Join(dir, "hosts", host.HostID+".crt"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	}
	// No CA or leaf private key is written: successful validation proves the
	// periodic checker needs only the public inventory.
	sender := &certificateNotifications{}
	return certificateWatch{PKIDir: dir, Hosts: func(context.Context) ([]pki.HostIdentity, error) { return hosts, nil }, Now: func() time.Time { return now }, Sender: sender}, sender
}

func TestCertificateWatchPublicInventoryAndExpiryBoundary(t *testing.T) {
	watch, sender := certificateWatchFixture(t, 10*365*24*time.Hour, certificateExpiryWarning+time.Second)
	require.NoError(t, watch.Validate(context.Background()))
	require.Empty(t, sender.messages)
	now := watch.Now()
	watch.Now = func() time.Time { return now.Add(time.Second) }
	require.NoError(t, watch.Validate(context.Background()))
	require.Len(t, sender.messages, 1)
	require.Equal(t, notification.SeverityWarning, sender.messages[0].Severity)
	require.Contains(t, sender.messages[0].Body, "host_gateway@control")
	require.Contains(t, sender.messages[0].Body, "host_gateway@storage")
	require.NotContains(t, sender.messages[0].Body, "moox_ca:")
	watch.Now = func() time.Time { return now.Add(certificateExpiryWarning + time.Second) }
	require.Error(t, watch.Validate(context.Background()))
	require.Equal(t, notification.SeverityCritical, sender.messages[len(sender.messages)-1].Severity)
	require.Contains(t, sender.messages[len(sender.messages)-1].Body, "expired")
}

func TestCertificateWatchConsumesDeploymentPKIInventoryWithoutPrivateKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "pki")
	store, err := pki.Open(dir)
	require.NoError(t, err)
	defer store.Close()
	_, err = store.EnsureCA()
	require.NoError(t, err)
	hosts := []pki.HostIdentity{{HostID: "control", Address: "192.0.2.1", PrivateAddress: "control.internal.example.test"}, {HostID: "storage", Address: "2001:db8::2"}}
	for _, host := range hosts {
		parent := filepath.Join(t.TempDir(), "private")
		require.NoError(t, os.Mkdir(parent, 0o700))
		_, err := store.Issue(host, filepath.Join(parent, "bundle"))
		require.NoError(t, err)
	}
	require.NoError(t, os.Remove(filepath.Join(dir, "ca.key")))
	watch := certificateWatch{PKIDir: dir, Hosts: func(context.Context) ([]pki.HostIdentity, error) { return hosts, nil }}
	require.NoError(t, watch.Validate(context.Background()))
}

func TestCertificateWatchReportsCAAndAllHostExpiry(t *testing.T) {
	watch, sender := certificateWatchFixture(t, certificateExpiryWarning, 60*24*time.Hour)
	require.NoError(t, watch.Validate(context.Background()))
	require.Len(t, sender.messages, 1)
	require.Equal(t, notification.SeverityWarning, sender.messages[0].Severity)
	for _, name := range []string{"moox_ca", "host_gateway@control", "host_gateway@storage"} {
		require.Contains(t, sender.messages[0].Body, name)
	}
}

func TestCertificateWatchReportsEveryMissingHostWithoutCreatingFiles(t *testing.T) {
	watch, sender := certificateWatchFixture(t, 365*24*time.Hour, 180*24*time.Hour)
	for _, host := range []string{"control", "storage"} {
		require.NoError(t, os.Remove(filepath.Join(watch.PKIDir, "hosts", host+".crt")))
	}
	require.ErrorContains(t, watch.Validate(context.Background()), "2 invalid")
	require.Len(t, sender.messages, 1)
	for _, host := range []string{"control", "storage"} {
		require.Contains(t, sender.messages[0].Body, "host_gateway@"+host)
		_, err := os.Lstat(filepath.Join(watch.PKIDir, "hosts", host+".crt"))
		require.True(t, os.IsNotExist(err))
	}
}

func TestCertificateWatchRejectsWrongCAHostSANAndInvalidInventory(t *testing.T) {
	for _, damage := range []string{"wrong-ca", "wrong-host", "changed-address", "bad-host-id", "missing-ca", "symlink-ca", "private-key", "not-yet-valid"} {
		t.Run(damage, func(t *testing.T) {
			watch, sender := certificateWatchFixture(t, 365*24*time.Hour, 180*24*time.Hour)
			caPath := filepath.Join(watch.PKIDir, "ca.crt")
			switch damage {
			case "wrong-ca":
				other, _ := certificateWatchFixture(t, 365*24*time.Hour, 180*24*time.Hour)
				raw, err := os.ReadFile(filepath.Join(other.PKIDir, "ca.crt"))
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(caPath, raw, 0o600))
			case "wrong-host":
				raw, err := os.ReadFile(filepath.Join(watch.PKIDir, "hosts", "storage.crt"))
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(watch.PKIDir, "hosts", "control.crt"), raw, 0o600))
			case "changed-address":
				hosts, err := watch.Hosts(context.Background())
				require.NoError(t, err)
				hosts[0].Address = "192.0.2.99"
				watch.Hosts = func(context.Context) ([]pki.HostIdentity, error) { return hosts, nil }
			case "bad-host-id":
				watch.Hosts = func(context.Context) ([]pki.HostIdentity, error) {
					return []pki.HostIdentity{{HostID: "../../ca.key", Address: "192.0.2.1"}}, nil
				}
			case "missing-ca":
				require.NoError(t, os.RemoveAll(watch.PKIDir))
			case "symlink-ca":
				require.NoError(t, os.Rename(caPath, caPath+".original"))
				require.NoError(t, os.Symlink(caPath+".original", caPath))
			case "private-key":
				require.NoError(t, os.WriteFile(caPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("fixture-do-not-echo")}), 0o600))
			case "not-yet-valid":
				now := watch.Now()
				watch.Now = func() time.Time { return now.Add(-2 * time.Hour) }
			}
			err := watch.Validate(context.Background())
			require.Error(t, err)
			require.Len(t, sender.messages, 1)
			require.Equal(t, notification.SeverityCritical, sender.messages[0].Severity)
			require.NotContains(t, sender.messages[0].Body+err.Error(), "fixture-do-not-echo")
			if damage == "missing-ca" {
				_, err := os.Lstat(watch.PKIDir)
				require.True(t, os.IsNotExist(err), "watching must not initialize PKI")
			}
		})
	}
}

func TestCertificateWatchEnvironmentUsesLiveAllHostInventoryAndOnlyCurrentCAs(t *testing.T) {
	mgr := setupBootstrapTestDB(t)
	db := mgr.GetDB()
	sqlDB, err := db.DB()
	require.NoError(t, err)
	defer sqlDB.Close()
	watch, _ := certificateWatchFixture(t, 365*24*time.Hour, 180*24*time.Hour)
	t.Setenv("MOOX_ADMIN_PKI_DIR", watch.PKIDir)
	t.Setenv("MOOX_NOTIFICATION_WEBHOOK_URL", "")
	t.Setenv("MOOX_EVENTBUS_CA_FILE", "fixture-eventbus-ca.crt")
	t.Setenv("MOOX_GATEWAY_CA_FILE", "obsolete-gateway-ca.crt")
	t.Setenv("MOOX_SERVICE_GATEWAY_CA_FILE", "obsolete-service-gateway-ca.crt")
	configured := newCertificateWatchFromEnvironment(db)
	require.Equal(t, watch.PKIDir, configured.PKIDir)
	require.Equal(t, []certificateWatchFile{{Name: "eventbus_ca", Path: "fixture-eventbus-ca.crt"}}, configured.Files)
	require.NoError(t, db.Exec("INSERT INTO t_hosts (c_host_id, c_address, c_status) VALUES ('control', '192.0.2.1', 'enabled'), ('storage', '2001:db8::2', 'disabled')").Error)
	hosts, err := configured.Hosts(context.Background())
	require.NoError(t, err)
	require.Len(t, hosts, 2, "disabled hosts still need valid certificates")
	require.NoError(t, db.Exec("INSERT INTO t_hosts (c_host_id, c_address) VALUES ('compute-1', '192.0.2.3')").Error)
	hosts, err = configured.Hosts(context.Background())
	require.NoError(t, err)
	require.Len(t, hosts, 3, "each run must query current registrations")
	t.Setenv("MOOX_ADMIN_PKI_DIR", "")
	t.Setenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE", "/fixture/secrets/admin-encryption.key")
	require.Equal(t, "/fixture/secrets/pki", newCertificateWatchFromEnvironment(db).PKIDir)
}

func TestCertificateWatchMissingHostInventoryDoesNotBlockControlStartup(t *testing.T) {
	watch, sender := certificateWatchFixture(t, 365*24*time.Hour, 180*24*time.Hour)
	require.NoError(t, os.Remove(filepath.Join(watch.PKIDir, "hosts", "storage.crt")))
	s := &server.Server{}
	s.AddService(certificateWatchTimerService, server.New(server.WithServiceName(certificateWatchTimerService), server.WithProtocol("timer")))
	require.NoError(t, registerCertificateWatchTimer(context.Background(), s, watch))
	require.Len(t, sender.messages, 1)
	require.Contains(t, sender.messages[0].Body, "host_gateway@storage")
	// Future timer runs continue to report the problem until issuance completes.
	require.Error(t, watch.Validate(context.Background()))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, registerCertificateWatchTimer(ctx, s, watch), context.Canceled)
}

func TestCertificateWatchRetainsSeparateEventBusCAAndBoundsNotifications(t *testing.T) {
	watch, sender := certificateWatchFixture(t, 365*24*time.Hour, 180*24*time.Hour)
	watch.Files = []certificateWatchFile{{Name: "eventbus_ca", Path: filepath.Join(watch.PKIDir, "missing-eventbus.crt")}}
	require.Error(t, watch.Validate(context.Background()))
	require.Contains(t, sender.messages[0].Body, "eventbus_ca")
	require.NotContains(t, sender.messages[0].Body, "gateway_peer_ca")
	require.NotContains(t, sender.messages[0].Body, "service_gateway_ca")
	sender.messages = nil
	findings := make([]certificateFinding, 1024)
	for i := range findings {
		findings[i] = certificateFinding{"host_gateway@" + strings.Repeat("a", 64), "host certificate does not match the MooX CA, server usage or registered SANs"}
	}
	watch.notify(context.Background(), notification.SeverityCritical, findings)
	require.Len(t, sender.messages, 1)
	require.LessOrEqual(t, utf8.RuneCountInString(sender.messages[0].Body), 4096)
	require.Contains(t, sender.messages[0].Body, "1024")
}

func TestReadPublicCertificateBundleRejectsUnboundedMalformedAndNonCertificateData(t *testing.T) {
	watch, _ := certificateWatchFixture(t, 365*24*time.Hour, 180*24*time.Hour)
	root, err := openPublicPKIRoot(watch.PKIDir)
	require.NoError(t, err)
	defer root.Close()
	original, err := os.ReadFile(filepath.Join(watch.PKIDir, "ca.crt"))
	require.NoError(t, err)
	for _, raw := range [][]byte{
		nil, []byte("fixture prefix\n" + string(original)), append(original, []byte("fixture trailing garbage")...),
		bytes.Repeat(original, 17),
		[]byte("-----BEGIN CERTIFICATE-----\ninvalid fixture\n-----END CERTIFICATE-----"),
		[]byte(strings.Repeat(" ", (1<<20)+1)),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: []byte("fixture")}),
	} {
		require.NoError(t, os.WriteFile(filepath.Join(watch.PKIDir, "invalid.crt"), raw, 0o600))
		_, err := readPublicCertificateBundle(root, "invalid.crt")
		require.Error(t, err)
	}
}
