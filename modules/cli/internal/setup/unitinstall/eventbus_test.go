package unitinstall

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/fsutil"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/unitbundle"
	"github.com/mooyang-code/moox/packages/jetstream"
	"github.com/stretchr/testify/require"
)

func eventBusProjectionFixture(t *testing.T) (string, string, PrepareOptions) {
	t.Helper()
	source, destination := privateParent(t), privateParent(t)
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	serverKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	serverTemplate := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: caTemplate.NotBefore, NotAfter: caTemplate.NotAfter, DNSNames: []string{"control.example.test"}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	serverDER, err := x509.CreateCertificate(rand.Reader, serverTemplate, caTemplate, &serverKey.PublicKey, caKey)
	require.NoError(t, err)
	keyDER, err := x509.MarshalECPrivateKey(serverKey)
	require.NoError(t, err)
	for name, raw := range map[string][]byte{
		"ca.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), "server.pem": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: serverDER}), "server-key.pem": pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), "users.yaml": []byte("users: []\n"),
	} {
		require.NoError(t, os.WriteFile(filepath.Join(source, name), raw, 0o600))
	}
	for role, tokenField := range map[string]string{"metrics-publisher": "token", "hostagent-publisher": "eventbus_token", "monitor-observability": "monitor_eventbus_token", "internal-admin": "token"} {
		raw := "version: 1\nurls: [tls://127.0.0.1:4222]\nusername: " + eventBusUsername(role) + "\n" + tokenField + ": synthetic-token-at-least-32-bytes-" + role + "\nca_file: ca.pem\n"
		require.NoError(t, os.WriteFile(filepath.Join(source, role+".yaml"), []byte(raw), 0o600))
	}
	for name, raw := range map[string]string{"eventbus/config/app.yaml": "broker: {cluster: {enabled: false}}\ninternal_client: {}\n", "monitor/config/app.yaml": "observability: {enabled: true}\n"} {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(destination, name)), 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(destination, name), []byte(raw), 0o600))
	}
	options := PrepareOptions{EventBusDirectory: source, EventBusURL: "tls://control.example.test:4222", MaterialOptions: unitbundle.Options{HostID: "control"}, Environment: map[string]map[string]string{"eventbus": {}, "host-gateway": {}, "host-agent": {}, "monitor": {}}}
	return source, destination, options
}

func TestEventBusProjectionKeepsRolesPrivateAndUsesReleaseCopies(t *testing.T) {
	source, destination, options := eventBusProjectionFixture(t)
	root, err := fsutil.OpenPhysicalRoot(destination, true)
	require.NoError(t, err)
	defer root.Close()
	require.NoError(t, projectEventBus(root, &options, []string{"host-gateway", "host-agent", "monitor", "eventbus"}, destination))
	for name, username := range map[string]string{"host-agent/config/eventbus.yaml": "hostagent-publisher", "host-gateway/secrets/eventbus/metrics-publisher.yaml": "metrics-publisher", "monitor/secrets/eventbus/monitor-observability.yaml": "monitor-observability-consumer", "eventbus/secrets/eventbus/internal-admin.yaml": "eventbus-internal-admin"} {
		credential, err := jetstream.LoadCredentialFile(filepath.Join(destination, name))
		require.NoError(t, err)
		require.Equal(t, username, credential.Username)
		require.Equal(t, []string{options.EventBusURL}, credential.URLs)
		require.True(t, strings.HasPrefix(credential.CAFile, destination+string(filepath.Separator)))
	}
	for _, forbidden := range []string{"host-agent/secrets/eventbus/metrics-publisher.yaml", "host-gateway/secrets/eventbus/internal-admin.yaml", "host-agent/secrets/eventbus/server-key.pem", "monitor/secrets/eventbus/hostagent-publisher.yaml", "monitor/secrets/eventbus/users.yaml"} {
		_, err := os.Lstat(filepath.Join(destination, forbidden))
		require.True(t, os.IsNotExist(err), forbidden)
	}
	require.FileExists(t, filepath.Join(destination, "eventbus/secrets/eventbus/server-key.pem"))
	raw, err := os.ReadFile(filepath.Join(destination, "eventbus/config/app.yaml"))
	require.NoError(t, err)
	document, err := yamlDocument(raw)
	require.NoError(t, err)
	require.Equal(t, "true", member(member(member(document, "broker"), "auth"), "enabled").Value)
	require.Equal(t, "true", member(member(member(document, "broker"), "tls"), "enabled").Value)
	require.NotContains(t, string(raw), source)
	ca, err := os.ReadFile(filepath.Join(destination, "monitor/secrets/eventbus/ca.pem"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(source, "ca.pem"), []byte("synthetic changed export"), 0o600))
	copy, err := os.ReadFile(filepath.Join(destination, "monitor/secrets/eventbus/ca.pem"))
	require.NoError(t, err)
	require.Equal(t, ca, copy, "a future export must not mutate a prepared release")
}

func TestEventBusProjectionRefusesIdentityAndConfigurationBypasses(t *testing.T) {
	for _, scenario := range []string{"plaintext", "server-name", "role", "public-file", "environment"} {
		t.Run(scenario, func(t *testing.T) {
			source, destination, options := eventBusProjectionFixture(t)
			components := []string{"eventbus"}
			switch scenario {
			case "plaintext":
				options.EventBusURL = "nats://control.example.test:4222"
			case "server-name":
				options.EventBusURL = "tls://wrong.example.test:4222"
			case "role":
				components = []string{"host-agent"}
				raw, err := os.ReadFile(filepath.Join(source, "metrics-publisher.yaml"))
				require.NoError(t, err)
				require.NoError(t, os.WriteFile(filepath.Join(source, "hostagent-publisher.yaml"), raw, 0o600))
			case "public-file":
				require.NoError(t, os.Chmod(filepath.Join(source, "ca.pem"), 0o644))
			case "environment":
				options.Environment["eventbus"]["MOOX_EVENTBUS_NATS_PASSWORD"] = "synthetic-private-marker"
			}
			root, err := fsutil.OpenPhysicalRoot(destination, true)
			require.NoError(t, err)
			defer root.Close()
			err = projectEventBus(root, &options, components, destination)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "synthetic-private-marker")
		})
	}
}

func TestStorageEventBusProjectionUsesRoleCredentialWithoutAppConfig(t *testing.T) {
	source, destination, options := eventBusProjectionFixture(t)
	raw := "version: 1\nusername: storage-eventbus\ntoken: " + strings.Repeat("s", 32) + "\nurls: [tls://unused.example.test:4222]\nca_file: ca.pem\n"
	require.NoError(t, os.WriteFile(filepath.Join(source, "storage-eventbus.yaml"), []byte(raw), 0o600))
	root, err := fsutil.OpenPhysicalRoot(destination, true)
	require.NoError(t, err)
	defer root.Close()
	components := []string{"storage-primary", "storage-node", "storage-view"}
	require.NoError(t, projectEventBus(root, &options, components, destination))
	for _, id := range components {
		values := options.Environment[id]
		credential, err := jetstream.LoadCredentialFile(values["MOOX_STORAGE_EVENTBUS_CREDENTIAL_FILE"])
		require.NoError(t, err)
		require.Equal(t, "storage-eventbus", credential.Username)
		require.Equal(t, []string{options.EventBusURL}, credential.URLs)
		require.Equal(t, options.EventBusURL, values["MOOX_STORAGE_EVENTBUS_URL"])
		require.FileExists(t, filepath.Join(destination, id, "secrets/eventbus/metrics-publisher.yaml"))
		require.NoFileExists(t, filepath.Join(destination, id, "secrets/eventbus/server-key.pem"))
	}
}
