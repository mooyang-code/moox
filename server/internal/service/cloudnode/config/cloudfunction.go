package config

import (
	"fmt"
	"os"

	"github.com/mooyang-code/moox/server/internal/service/cloudnode/constants"

	"gopkg.in/yaml.v2"
)

// CloudFunctionConfig 云函数配置
type CloudFunctionConfig struct {
	CloudFunction CloudFunctionSettings `yaml:"cloudfunction"`
}

// CloudFunctionSettings 云函数具体设置
type CloudFunctionSettings struct {
	ZipFilePath       string            `yaml:"zip_file_path"`
	DefaultTimeout    int               `yaml:"default_timeout"`
	DefaultMemorySize int               `yaml:"default_memory_size"`
	DefaultEnvVars    map[string]string `yaml:"default_env_vars"`
}

var globalCloudFunctionConfig *CloudFunctionConfig

// LoadCloudFunctionConfig 加载云函数配置文件
func LoadCloudFunctionConfig() (*CloudFunctionConfig, error) {
	if globalCloudFunctionConfig != nil {
		return globalCloudFunctionConfig, nil
	}

	configPath := "./config/cloudfunction.yaml"
	yamlFile, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取云函数配置文件失败: %+v", err)
	}

	var config CloudFunctionConfig
	if err := yaml.Unmarshal(yamlFile, &config); err != nil {
		return nil, fmt.Errorf("解析云函数YAML配置失败: %+v", err)
	}

	globalCloudFunctionConfig = &config
	return &config, nil
}

// GetCloudFunctionConfig 获取全局云函数配置
func GetCloudFunctionConfig() *CloudFunctionConfig {
	if globalCloudFunctionConfig == nil {
		config, err := LoadCloudFunctionConfig()
		if err != nil {
			// 如果加载失败，返回默认配置
			return &CloudFunctionConfig{
				CloudFunction: CloudFunctionSettings{
					ZipFilePath:       constants.GetDefaultZipFilePath(),
					DefaultTimeout:    30,
					DefaultMemorySize: 128,
					DefaultEnvVars: map[string]string{
						"NODE_ENV": "production",
					},
				},
			}
		}
		return config
	}
	return globalCloudFunctionConfig
}

// GetZipFilePath 获取云函数代码包路径
func (c *CloudFunctionConfig) GetZipFilePath() string {
	return c.CloudFunction.ZipFilePath
}

// GetDefaultTimeout 获取默认超时时间
func (c *CloudFunctionConfig) GetDefaultTimeout() int {
	return c.CloudFunction.DefaultTimeout
}

// GetDefaultMemorySize 获取默认内存大小
func (c *CloudFunctionConfig) GetDefaultMemorySize() int {
	return c.CloudFunction.DefaultMemorySize
}

// GetDefaultEnvVars 获取默认环境变量
func (c *CloudFunctionConfig) GetDefaultEnvVars() map[string]string {
	return c.CloudFunction.DefaultEnvVars
}
