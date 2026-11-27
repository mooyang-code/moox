package bootstrap

import (
	"context"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/admin/internal/config"
	"github.com/mooyang-code/moox/modules/admin/internal/console"
	"github.com/mooyang-code/moox/modules/admin/internal/service/auth"
	"github.com/mooyang-code/moox/modules/admin/internal/webserver"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gopkg.in/yaml.v3"
	"trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/log"
)

// Config 应用配置集合（单次加载 app.yaml，显式持有主密钥）。
type Config struct {
	App           *config.AppConfig
	Auth          *auth.Config
	Console       *console.Config
	AdminNodeID   string
	EncryptionKey string
	// Web 是浏览器入口（9527）的部署参数。
	Web webserver.Config
}

// LoadConfigs 加载系统配置：configPath 为单一配置文件 app.yaml（-config）。
func LoadConfigs(ctx context.Context, configPath string) (*Config, error) {
	log.Info("正在加载应用配置...")

	appCfg, err := config.Load(configPath)
	if err != nil {
		return nil, err
	}
	encryptionKey, err := loadEncryptionKey()
	if err != nil {
		return nil, err
	}
	// 校验必须针对 tRPC 实际加载的配置文件（-conf 可指定其他路径）；NewServer 已解析该参数，
	// 此时尚未 Serve，不会有监听被提前打开。
	listenerConfig := trpc.ServerConfigPath
	if err := validateSetupListener(listenerConfig); err != nil {
		return nil, err
	}
	if err := validateGatewayControlListener(listenerConfig); err != nil {
		return nil, err
	}
	adminNodeID := strings.TrimSpace(os.Getenv("MOOX_ADMIN_NODE_ID"))
	if !servicecatalog.ValidHostID(adminNodeID) {
		return nil, fmt.Errorf("MOOX_ADMIN_NODE_ID must be a canonical control host ID")
	}
	web, err := loadWebConfig(appCfg.Database.Path)
	if err != nil {
		return nil, err
	}

	authCfg := &auth.Config{
		Session:  appCfg.Session,
		Security: appCfg.Security,
	}
	consoleCfg := &console.Config{
		Console:   appCfg.Console,
		RateLimit: appCfg.RateLimit,
	}
	console.SetConfig(consoleCfg)
	log.Info("应用配置加载成功")

	return &Config{
		App:           appCfg,
		Auth:          authCfg,
		Console:       consoleCfg,
		AdminNodeID:   adminNodeID,
		EncryptionKey: encryptionKey,
		Web:           web,
	}, nil
}

// loadWebConfig 读浏览器入口的部署参数：控制机地址 MOOX_ADMIN_WEB_ADDRESS（必填）、监听地址 MOOX_ADMIN_WEB_LISTEN
// （默认 0.0.0.0:9527）；网页证书用 MooX CA 签，放在 Admin 数据目录（数据库所在目录）下的 web-cert/。
func loadWebConfig(databasePath string) (webserver.Config, error) {
	address := strings.TrimSpace(os.Getenv("MOOX_ADMIN_WEB_ADDRESS"))
	if !servicecatalog.ValidHostAddress(address) {
		return webserver.Config{}, fmt.Errorf("MOOX_ADMIN_WEB_ADDRESS 要写控制机地址（IP 或域名，不带端口），当前是 %q", address)
	}
	listen := strings.TrimSpace(os.Getenv("MOOX_ADMIN_WEB_LISTEN"))
	if listen == "" {
		listen = webserver.DefaultListen
	}
	return webserver.Config{Listen: listen, Address: address, PKIDir: adminPKIDir(), CertDir: filepath.Join(filepath.Dir(databasePath), "web-cert")}, nil
}

func validateGatewayControlListener(path string) error {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read gateway control listener config: %w", err)
	}
	var document struct {
		Server struct {
			Services []struct {
				Name     string `yaml:"name"`
				IP       string `yaml:"ip"`
				Port     int    `yaml:"port"`
				Address  string `yaml:"address"`
				Nic      string `yaml:"nic"`
				Network  string `yaml:"network"`
				Protocol string `yaml:"protocol"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("decode gateway control listener config")
	}
	count := 0
	for _, listener := range document.Server.Services {
		if listener.Name != servicecatalog.GatewayControlPath {
			continue
		}
		count++
		ip := net.ParseIP(listener.IP)
		if ip == nil || !ip.IsLoopback() || listener.Port != 11112 || listener.Network != "tcp" || listener.Protocol != "trpc" || listener.Address != "" || listener.Nic != "" {
			return fmt.Errorf("gateway control listener must bind tcp trpc to loopback port 11112 without address or NIC override")
		}
	}
	if count != 1 {
		return fmt.Errorf("exactly one gateway control listener is required")
	}
	return nil
}

func validateSetupListener(path string) error {
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read setup listener config: %w", err)
	}
	var document struct {
		Server struct {
			Services []struct {
				Name     string `yaml:"name"`
				IP       string `yaml:"ip"`
				Port     int    `yaml:"port"`
				Address  string `yaml:"address"`
				Nic      string `yaml:"nic"`
				Network  string `yaml:"network"`
				Protocol string `yaml:"protocol"`
			} `yaml:"service"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return fmt.Errorf("decode setup listener config")
	}
	// tRPC 对重名服务以后者覆盖前者，所以必须恰好一项，并校验这一项的全部字段。
	count := 0
	for _, service := range document.Server.Services {
		if service.Name != "trpc.moox.admin.Setup" {
			continue
		}
		count++
		address := net.ParseIP(strings.TrimSpace(service.IP))
		if address == nil || !address.IsLoopback() {
			return fmt.Errorf("setup listener must bind to loopback")
		}
		if service.Port != 11110 || service.Network != "tcp" || service.Protocol != "trpc" {
			return fmt.Errorf("setup listener must use tcp trpc on port 11110")
		}
		// tRPC 优先使用 address / nic，会绕过上面的 loopback 校验；Setup 没有签名鉴权，不允许覆盖。
		if strings.TrimSpace(service.Address) != "" || strings.TrimSpace(service.Nic) != "" {
			return fmt.Errorf("setup listener must not override the listen address or NIC")
		}
	}
	if count == 0 {
		return fmt.Errorf("setup listener trpc.moox.admin.Setup is required")
	}
	if count != 1 {
		return fmt.Errorf("exactly one setup listener is required")
	}
	return nil
}

// loadEncryptionKey 读取主密钥并返回，不修改进程环境变量。
func loadEncryptionKey() (string, error) {
	if key := strings.TrimSpace(os.Getenv("MOOX_ADMIN_ENCRYPTION_KEY")); key != "" {
		return key, nil
	}
	path := strings.TrimSpace(os.Getenv("MOOX_ADMIN_ENCRYPTION_KEY_FILE"))
	if path == "" {
		return "", fmt.Errorf("MOOX_ADMIN_ENCRYPTION_KEY_FILE is required in server mode")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("stat admin encryption key file: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
		return "", fmt.Errorf("admin encryption key file must be a regular 0600 file")
	}
	raw, err := os.ReadFile(filepath.Clean(path))
	key := strings.TrimSpace(string(raw))
	if err != nil || key == "" {
		if err == nil {
			err = fmt.Errorf("file is empty")
		}
		return "", fmt.Errorf("read admin encryption key file: %w", err)
	}
	return key, nil
}
