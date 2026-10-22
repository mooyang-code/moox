package scfpack

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// 这些用例和打包脚本契约测试（scripts/build/build-collector-scf-package_test.sh）里的写法一一对应。
func TestCheckConfigRejectsCredentials(t *testing.T) {
	rejected := map[string]string{
		"nats-jwt":                      "credentials: |\n  -----BEGIN NATS USER JWT-----\n  test-user-jwt\n  ------END NATS USER JWT------\n",
		"nkey-seed":                     "nats:\n  user_nkey_seed: SUABCDEF1234567890\n",
		"scalar":                        "note: MOOX_STORAGE_PRIMARY_AUTH_SECRET=topsecret\n",
		"list":                          "notes:\n  - MOOX_EVENTBUS_NATS_PASSWORD=secret\n",
		"structured":                    "env:\n  - name: MOOX_STORAGE_PRIMARY_AUTH_SECRET\n    value: topsecret\n",
		"structured-cased":              "env:\n  - Name: MOOX_EVENTBUS_NATS_PASSWORD\n    Value: topsecret\n",
		"null-key":                      "MOOX_STORAGE_PRIMARY_AUTH_SECRET=topsecret:\n",
		"comment":                       "# MOOX_STORAGE_PRIMARY_AUTH_SECRET=topsecret\n",
		"quoted-comment":                "# \"MOOX_STORAGE_PRIMARY_AUTH_SECRET\": \"topsecret\"\n",
		"quoted-punct-comment":          "# \"MOOX_STORAGE_PRIMARY_AUTH_SECRET\": \",topsecret\"\n",
		"empty-quote-tail":              "# MOOX_STORAGE_PRIMARY_AUTH_SECRET: \"\"topsecret\n",
		"null-tail":                     "# MOOX_STORAGE_PRIMARY_AUTH_SECRET: null topsecret\n",
		"tilde-tail":                    "# MOOX_STORAGE_PRIMARY_AUTH_SECRET: ~ topsecret\n",
		"null-plus-comment-credential":  "# MOOX_STORAGE_PRIMARY_AUTH_SECRET: null # MOOX_EVENTBUS_NATS_PASSWORD=topsecret\n",
		"empty-plus-comment-credential": "# MOOX_STORAGE_PRIMARY_AUTH_SECRET: \"\" # MOOX_EVENTBUS_NATS_PASSWORD=topsecret\n",
		"private-key":                   "pem: |\n  -----BEGIN RSA PRIVATE KEY-----\n",
		"sensitive-field":               "storage:\n  app_key: abc\n",
		"alias":                         "a: &x 1\nb: *x\n",
		"anchor":                        "a: &x 1\n",
		"duplicate-key":                 "a: 1\na: 2\n",
		"invalid-yaml":                  "a: [1, 2\n",
	}
	for name, content := range rejected {
		require.Error(t, CheckConfig([]byte(content)), name)
	}
}

func TestCheckConfigAllowsEmptyPlaceholders(t *testing.T) {
	allowed := map[string]string{
		"null":              "MOOX_STORAGE_PRIMARY_AUTH_SECRET: null\n",
		"tilde":             "MOOX_STORAGE_PRIMARY_AUTH_SECRET: ~\n",
		"empty":             "MOOX_STORAGE_PRIMARY_AUTH_SECRET: \"\"\n",
		"quoted-whitespace": "MOOX_STORAGE_PRIMARY_AUTH_SECRET: '  '\n",
		"implicit-null-eof": "MOOX_STORAGE_PRIMARY_AUTH_SECRET:",
		"empty-app-key":     "storage:\n  app_key: \"\"\n  items:\n    - x\n",
		"service-auth":      "system:\n  service_auth:\n    access_key: public-id\n",
		"empty-document":    "",
		"two-documents":     "a: 1\n---\nb: 2\n",
	}
	for name, content := range allowed {
		require.NoError(t, CheckConfig([]byte(content)), name)
	}
}

func TestCheckPackage(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "sources", "market"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "main"), []byte("binary"), 0o755))
	good := filepath.Join(root, "sources", "market", "binance.yaml")
	require.NoError(t, os.WriteFile(good, []byte("app_key: \"\"\n"), 0o644))
	require.NoError(t, CheckPackage(root))

	bad := filepath.Join(root, "sources", "market", "credential.yaml")
	require.NoError(t, os.WriteFile(bad, []byte("note: MOOX_STORAGE_PRIMARY_AUTH_SECRET=topsecret\n"), 0o644))
	require.ErrorContains(t, CheckPackage(root), "sources/market/credential.yaml")
	require.NoError(t, os.Remove(bad))

	require.NoError(t, os.WriteFile(filepath.Join(root, "notes.txt"), []byte("x"), 0o644))
	require.ErrorContains(t, CheckPackage(root), "unexpected SCF configuration payload")
	require.NoError(t, os.Remove(filepath.Join(root, "notes.txt")))

	require.NoError(t, os.Symlink(good, filepath.Join(root, "link.yaml")))
	require.ErrorContains(t, CheckPackage(root), "symlinks are not permitted")
}

func TestNormpathIsLexical(t *testing.T) {
	require.Equal(t, "/a/config", Normpath("/a/link/../config"))
	require.Equal(t, "cfg", Normpath("cfg/./"))
}
