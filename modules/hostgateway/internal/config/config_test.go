package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const storageYAML = `host:
  id: storage
tls:
  cert_file: ../../certs/host-gateway/server.crt
  key_file: ../../certs/host-gateway/server.key
  ca_file: ../../certs/moox-ca.crt
control:
  target: 106.53.107.122:11003
  caller: host-gateway@storage
  key_file: ../../secrets/caller-host-gateway.key
store:
  path: ../../data/host-gateway
`

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "release", "host-gateway", "config")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "app.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoadAppliesDefaultsAndResolvesPaths(t *testing.T) {
	path := writeConfig(t, storageYAML)
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.RemoteAddr != RemoteAddress || cfg.Server.LocalAddr != LocalAddress || cfg.Server.HealthAddr != HealthAddress {
		t.Fatalf("默认监听地址不对: %+v", cfg.Server)
	}
	root := filepath.Dir(filepath.Dir(filepath.Dir(path)))
	if cfg.TLS.CAFile != filepath.Join(root, "certs", "moox-ca.crt") || cfg.Store.Path != filepath.Join(root, "data", "host-gateway") {
		t.Fatalf("相对路径应当按配置文件所在目录解析: %+v %+v", cfg.TLS, cfg.Store)
	}
	if cfg.Direct() {
		t.Fatal("storage 不应直连网关控制")
	}
}

// controlYAML 是 control 主机的配置：直连本机网关控制，不需要调用方密钥。
var controlYAML = strings.NewReplacer("id: storage", "id: control", "target: 106.53.107.122:11003", "target: 127.0.0.1:11112",
	"caller: host-gateway@storage", "caller: host-gateway@control", "  key_file: ../../secrets/caller-host-gateway.key\n", "").Replace(storageYAML)

func TestLoadControlDirectTarget(t *testing.T) {
	cfg, err := Load(writeConfig(t, controlYAML))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Direct() {
		t.Fatal("control 应当直连本机网关控制")
	}
}

func TestLoadRejectsInvalidConfiguration(t *testing.T) {
	cases := map[string]struct{ base, old, new, want string }{
		"缺少主机 ID":           {storageYAML, "id: storage", "id: ''", "host.id"},
		"调用方与主机不一致":         {storageYAML, "caller: host-gateway@storage", "caller: host-gateway@control", "control.caller"},
		"其他主机缺少调用方密钥":       {storageYAML, "  key_file: ../../secrets/caller-host-gateway.key\n", "", "control.key_file"},
		"control 经网络访问网关控制": {controlYAML, "target: 127.0.0.1:11112", "target: 10.0.0.1:11112", "只能直连本机"},
		"control 配置了用不到的密钥": {controlYAML, "store:", "  key_file: caller.key\nstore:", "不使用 control.key_file"},
		"本机入口不是回环地址":        {storageYAML, "store:", "server:\n  local_addr: 0.0.0.0:11002\nstore:", "local_addr"},
		"缺少证书":              {storageYAML, "  cert_file: ../../certs/host-gateway/server.crt\n", "", "tls.cert_file"},
		"未知字段":              {storageYAML, "store:", "proxy:\n  max_body_bytes: 1\nstore:", "field proxy not found"},
		"目标不是 host:port":    {storageYAML, "target: 106.53.107.122:11003", "target: 106.53.107.122", "control.target"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			content := strings.Replace(tc.base, tc.old, tc.new, 1)
			if content == tc.base {
				t.Fatalf("用例没有改动配置: %q", tc.old)
			}
			_, err := Load(writeConfig(t, content))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望报错包含 %q，实际 %v", tc.want, err)
			}
		})
	}
}
