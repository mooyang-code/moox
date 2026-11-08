// Package objectstore 是归档的对象存储客户端：只用标准 S3 接口（单次 PutObject、HeadObject、GetObject），
// 不出现任何厂商专有的接口、请求头或 SDK，后端（腾讯云 COS、R2、自建 S3 兼容存储）只是配置里的一个 endpoint。
package objectstore

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"net"
	"os"
	"regexp"
	"strings"
)

// DefaultPrefix 是对象键的默认前缀；权限策略也按它配置。
const DefaultPrefix = "moox-archive/"

// 地址风格。
const (
	URLStyleVirtual = "virtual"
	URLStylePath    = "path"
)

// Config 是对象存储的连接配置；整段省略表示不使用对象存储。
type Config struct {
	// Endpoint 必填：主机名[:端口]，不带协议和路径。
	Endpoint string `yaml:"endpoint"`
	// Region 必填：COS 填地域（ap-nanjing），R2 填 auto。为空时 minio-go 会先查桶位置，受限的子账号没有这个权限。
	Region string `yaml:"region"`
	// Bucket 必填：COS 桶名带 APPID。
	Bucket string `yaml:"bucket"`
	// Prefix 默认 moox-archive/。
	Prefix string `yaml:"prefix"`
	// URLStyle 必填：virtual 或 path，没有默认值（自动判断对 COS 会选错）。
	URLStyle string `yaml:"url_style"`
	// UseTLS 默认 true。
	UseTLS *bool `yaml:"use_tls"`
	// StorageClass 可选，按 x-amz-storage-class 发送；留空用桶的默认。
	StorageClass string `yaml:"storage_class"`
	// CredentialsFile 必填：凭据文件路径，相对 worker 配置文件所在目录。
	CredentialsFile string `yaml:"credentials_file"`
}

var (
	bucketPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	prefixPattern       = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)
	storageClassPattern = regexp.MustCompile(`^[A-Z_]{1,32}$`)
	hostnamePattern     = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?$`)
)

// TLS 报告是否使用 HTTPS（未配置时默认是）。
func (c Config) TLS() bool { return c.UseTLS == nil || *c.UseTLS }

// Validate 补上默认值（prefix）并校验配置；不读凭据文件。
func (c *Config) Validate() error {
	if c.Prefix == "" {
		c.Prefix = DefaultPrefix
	}
	endpoint := c.Endpoint
	if endpoint == "" || strings.Contains(endpoint, "://") || strings.ContainsAny(endpoint, "/ \t\r\n") {
		return errors.New("object_store.endpoint 必须是 主机名[:端口]，不带协议、路径和空白")
	}
	host := endpoint
	if h, port, err := net.SplitHostPort(endpoint); err == nil {
		if port == "" {
			return errors.New("object_store.endpoint 的端口不能为空")
		}
		host = h
	} else if strings.Contains(endpoint, ":") {
		return fmt.Errorf("object_store.endpoint %q 无法解析: %v", endpoint, err)
	}
	if net.ParseIP(host) == nil && !hostnamePattern.MatchString(host) {
		return fmt.Errorf("object_store.endpoint 的主机名 %q 不合法", host)
	}
	if strings.TrimSpace(c.Region) == "" || strings.ContainsAny(c.Region, " \t\r\n") {
		return errors.New("object_store.region 不能为空（COS 填地域，如 ap-nanjing；R2 填 auto）")
	}
	if !bucketPattern.MatchString(c.Bucket) {
		return fmt.Errorf("object_store.bucket %q 不合法：只能是小写字母、数字、点和连字符，3～63 个字符", c.Bucket)
	}
	if err := checkPrefix(c.Prefix); err != nil {
		return err
	}
	if c.URLStyle != URLStyleVirtual && c.URLStyle != URLStylePath {
		return errors.New("object_store.url_style 必须写明 virtual 或 path（COS 新建的桶只接受 virtual）")
	}
	if c.StorageClass != "" && !storageClassPattern.MatchString(c.StorageClass) {
		return fmt.Errorf("object_store.storage_class %q 不合法：只能是大写字母与下划线，例如 STANDARD_IA", c.StorageClass)
	}
	if strings.TrimSpace(c.CredentialsFile) == "" {
		return errors.New("object_store.credentials_file 不能为空")
	}
	return nil
}

func checkPrefix(prefix string) error {
	switch {
	case !strings.HasSuffix(prefix, "/") || strings.HasPrefix(prefix, "/"):
		return fmt.Errorf("object_store.prefix %q 必须以 / 结尾、不能以 / 开头", prefix)
	case !prefixPattern.MatchString(prefix):
		return fmt.Errorf("object_store.prefix %q 只能包含字母、数字和 . _ / -", prefix)
	case strings.Contains(prefix, "//"):
		return fmt.Errorf("object_store.prefix %q 不能包含 //", prefix)
	}
	for _, part := range strings.Split(strings.TrimSuffix(prefix, "/"), "/") {
		if part == "." || part == ".." {
			return fmt.Errorf("object_store.prefix %q 不能包含 . 或 .. 片段", prefix)
		}
	}
	return nil
}

// Credentials 是 S3 访问密钥。
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
}

// LoadCredentials 读取凭据文件：只认 ACCESS_KEY_ID 与 SECRET_ACCESS_KEY 两个键，各出现一次；允许空行和 # 注释，
// 不认引号和 export，值里不能有空白。文件必须是普通文件（不是符号链接），组和其他人都没有权限。错误信息不回显值。
func LoadCredentials(path string) (Credentials, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("读取对象存储凭据文件 %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return Credentials{}, fmt.Errorf("对象存储凭据文件 %s 必须是普通文件（不能是符号链接）", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Credentials{}, fmt.Errorf("对象存储凭据文件 %s 的权限是 %#o，组和其他人不能有权限（应为 0600）", path, info.Mode().Perm())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, fmt.Errorf("读取对象存储凭据文件 %s: %w", path, err)
	}
	defer clear(raw)
	values := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return Credentials{}, fmt.Errorf("对象存储凭据文件 %s 第 %d 行不是 键=值", path, line)
		}
		if key != "ACCESS_KEY_ID" && key != "SECRET_ACCESS_KEY" {
			return Credentials{}, fmt.Errorf("对象存储凭据文件 %s 第 %d 行的键不认识（只认 ACCESS_KEY_ID、SECRET_ACCESS_KEY）", path, line)
		}
		if _, dup := values[key]; dup {
			return Credentials{}, fmt.Errorf("对象存储凭据文件 %s 第 %d 行的 %s 重复", path, line, key)
		}
		if value == "" || strings.ContainsAny(value, " \t\"'") {
			return Credentials{}, fmt.Errorf("对象存储凭据文件 %s 第 %d 行的 %s 为空或含空白、引号", path, line, key)
		}
		values[key] = value
	}
	if err := scanner.Err(); err != nil {
		return Credentials{}, fmt.Errorf("读取对象存储凭据文件 %s: %w", path, err)
	}
	for _, key := range []string{"ACCESS_KEY_ID", "SECRET_ACCESS_KEY"} {
		if values[key] == "" {
			return Credentials{}, fmt.Errorf("对象存储凭据文件 %s 缺少 %s", path, key)
		}
	}
	return Credentials{AccessKeyID: values["ACCESS_KEY_ID"], SecretAccessKey: values["SECRET_ACCESS_KEY"]}, nil
}
