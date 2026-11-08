package config

import (
	"fmt"
	"strings"
)

// ArchiveObjectStore 是归档 worker 的对象存储副本（[archive_object_store]，可选）：标准 S3 接口，后端可替换。
// 这里不含任何密钥，secret_id 指向管理台“设置 › 密钥”里那条（provider s3、类型 api_key）；
// 部署 archive-worker 时由 moox-cli 从 Admin 取出并投影成 worker 的凭据文件。严格校验留给 worker 启动（check-config）。
type ArchiveObjectStore struct {
	Endpoint     string `toml:"endpoint"`
	Region       string `toml:"region"`
	Bucket       string `toml:"bucket"`
	Prefix       string `toml:"prefix"`
	URLStyle     string `toml:"url_style"`
	UseTLS       *bool  `toml:"use_tls"`
	StorageClass string `toml:"storage_class"`
	SecretID     string `toml:"secret_id"`
}

// TLS 报告是否使用 HTTPS（未写时默认是）。
func (s ArchiveObjectStore) TLS() bool { return s.UseTLS == nil || *s.UseTLS }

// Validate 校验必填项：endpoint、region、bucket、url_style（virtual 或 path）、secret_id，endpoint 不带协议。
func (s *ArchiveObjectStore) Validate() error {
	s.Endpoint, s.Region, s.Bucket = strings.TrimSpace(s.Endpoint), strings.TrimSpace(s.Region), strings.TrimSpace(s.Bucket)
	s.Prefix, s.URLStyle, s.StorageClass, s.SecretID = strings.TrimSpace(s.Prefix), strings.TrimSpace(s.URLStyle), strings.TrimSpace(s.StorageClass), strings.TrimSpace(s.SecretID)
	for _, field := range []struct{ name, value string }{
		{"endpoint", s.Endpoint}, {"region", s.Region}, {"bucket", s.Bucket}, {"url_style", s.URLStyle}, {"secret_id", s.SecretID},
	} {
		if field.value == "" {
			return fmt.Errorf("config_invalid: archive_object_store.%s 不能为空", field.name)
		}
	}
	if strings.Contains(s.Endpoint, "://") || strings.ContainsAny(s.Endpoint, "/ \t") {
		return fmt.Errorf("config_invalid: archive_object_store.endpoint 只写 主机名[:端口]，不带 https:// 和路径")
	}
	if s.URLStyle != "virtual" && s.URLStyle != "path" {
		return fmt.Errorf("config_invalid: archive_object_store.url_style 只能是 virtual 或 path（COS 新建的桶只接受 virtual）")
	}
	if s.Prefix != "" && !strings.HasSuffix(s.Prefix, "/") {
		return fmt.Errorf("config_invalid: archive_object_store.prefix 必须以 / 结尾")
	}
	return nil
}
