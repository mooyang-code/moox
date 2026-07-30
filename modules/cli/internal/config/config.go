package config

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v2"
)

// MooxConfig moox服务配置
type MooxConfig struct {
	AuthTarget string `yaml:"auth_target"` // 认证服务地址
}

// Config CLI 配置。
type Config struct {
	Storage struct {
		Target string `yaml:"target"`
	} `yaml:"storage"`

	MooX *MooxConfig `yaml:"moox"` // moox服务配置

	Doctor DoctorConfig `yaml:"doctor"`
}

type DoctorConfig struct {
	MonitorTarget           string `yaml:"monitor_target"`
	SysDeployTarget         string `yaml:"sysdeploy_target"`
	NodeID                  string `yaml:"node_id"`
	ReleaseRoot             string `yaml:"release_root"`
	SeedPath                string `yaml:"seed_path"`
	DatasetHealthPolicyPath string `yaml:"dataset_health_policy_path"`
}

func (c *Config) EffectiveDoctor() DoctorConfig {
	value := DoctorConfig{
		MonitorTarget: "ip://127.0.0.1:11410", SysDeployTarget: "ip://127.0.0.1:11109",
		ReleaseRoot: ".", SeedPath: "examples/setup/default/service-deployments.yaml",
		DatasetHealthPolicyPath: "config/dataset-health-policy.yaml",
	}
	if c != nil {
		mergeDoctor(&value, c.Doctor)
	}
	overrideDoctorFromEnv(&value)
	if value.NodeID == "" {
		value.NodeID, _ = os.Hostname()
	}
	return value
}

func mergeDoctor(target *DoctorConfig, source DoctorConfig) {
	if source.MonitorTarget != "" {
		target.MonitorTarget = source.MonitorTarget
	}
	if source.SysDeployTarget != "" {
		target.SysDeployTarget = source.SysDeployTarget
	}
	if source.NodeID != "" {
		target.NodeID = source.NodeID
	}
	if source.ReleaseRoot != "" {
		target.ReleaseRoot = source.ReleaseRoot
	}
	if source.SeedPath != "" {
		target.SeedPath = source.SeedPath
	}
	if source.DatasetHealthPolicyPath != "" {
		target.DatasetHealthPolicyPath = source.DatasetHealthPolicyPath
	}
}

func overrideDoctorFromEnv(value *DoctorConfig) {
	for name, target := range map[string]*string{
		"MOOX_DOCTOR_MONITOR_TARGET": &value.MonitorTarget, "MOOX_DOCTOR_SYSDEPLOY_TARGET": &value.SysDeployTarget,
		"MOOX_NODE_ID": &value.NodeID, "MOOX_RELEASE_ROOT": &value.ReleaseRoot,
		"MOOX_SERVICE_DEPLOYMENTS_SEED": &value.SeedPath,
		"MOOX_DATASET_HEALTH_POLICY":    &value.DatasetHealthPolicyPath,
	} {
		if raw := os.Getenv(name); raw != "" {
			*target = raw
		}
	}
}

// getConfigPaths 获取可能的配置文件路径列表
func getConfigPaths() []string {
	paths := []string{
		// 当前目录
		"./config/cli.yaml",
		"./cli.yaml",
		// 上级目录
		"../config/cli.yaml",
		// 相对路径（用于构建后的二进制）
		"./config/cli.yaml",
		// 系统配置目录
		"/etc/moox/cli.yaml",
		// 用户家目录
		filepath.Join(os.Getenv("HOME"), ".moox", "cli.yaml"),
	}

	// 添加环境变量指定的配置文件
	if configPath := os.Getenv("MOOX_CONFIG"); configPath != "" {
		paths = append([]string{configPath}, paths...)
	}

	return paths
}

// LoadConfig 加载配置文件
func LoadConfig() (*Config, error) {
	var config Config
	var lastErr error

	// 尝试从多个可能的路径加载配置文件
	for _, configPath := range getConfigPaths() {
		yamlFile, err := os.ReadFile(configPath)
		if err != nil {
			lastErr = err
			continue // 尝试下一个路径
		}

		// 解析YAML到Config结构
		if err := yaml.Unmarshal(yamlFile, &config); err != nil {
			lastErr = fmt.Errorf("解析YAML失败 (%s): %v", configPath, err)
			continue
		}

		// 成功加载配置
		fmt.Fprintf(os.Stderr, "\033[32m✅ 成功加载配置文件: %s\033[0m\n", configPath)
		return &config, nil
	}

	// 所有路径都失败了
	return nil, fmt.Errorf("\033[91m⚠️  警告：加载配置失败: 无法找到配置文件，尝试的路径: %v，最后的错误: %v\033[0m", getConfigPaths(), lastErr)
}
