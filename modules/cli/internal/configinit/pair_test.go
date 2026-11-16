package configinit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
)

// sampleRendered 是 sampleRequest 渲染出的两份文件。
func sampleRendered(t *testing.T) Rendered {
	t.Helper()
	_, rendered := renderPair(t, sampleRequest(), nil)
	return rendered
}

// writePairFiles 在 dir 下写出明文的 moox.toml 与用 keys 加密的 moox.secrets.toml，返回 moox.toml 的路径。
func writePairFiles(t *testing.T, dir string, rendered Rendered, keys configcrypt.KeyStore) string {
	t.Helper()
	configPath := filepath.Join(dir, "moox.toml")
	if err := os.WriteFile(configPath, rendered.Config, 0o600); err != nil {
		t.Fatal(err)
	}
	sealed, err := configcrypt.Encrypt(rendered.Secrets, keys)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, topology.SecretsFileName), sealed, 0o600); err != nil {
		t.Fatal(err)
	}
	return configPath
}

// pairFixture 写出 sampleRequest 的两份文件，返回 moox.toml、moox.secrets.toml 的路径与测试密钥。
func pairFixture(t *testing.T) (configPath, secretsPath string, keys *configcrypt.MemoryStore) {
	t.Helper()
	keys = &configcrypt.MemoryStore{}
	configPath = writePairFiles(t, t.TempDir(), sampleRendered(t), keys)
	return configPath, topology.SecretsPathFor(configPath), keys
}

// sealedBundle 是两份文件的备份信封（与 backupPlain 上传的一致）。
func sealedBundle(t *testing.T, rendered Rendered, password string) string {
	t.Helper()
	plain, err := json.Marshal(backupBundle{Config: string(rendered.Config), Secrets: string(rendered.Secrets)})
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := configcrypt.SealBackup(plain, password)
	if err != nil {
		t.Fatal(err)
	}
	return envelope
}
