// Package scfpack 检查要打进 SCF 云函数包的配置：必须是合法 YAML、没有别名/锚点和重复键、不含任何凭据。
// 打包脚本（scripts/build/build-collector-scf-package.sh）在 zip 之前调用它；凭据绝不能进入发出去的包。
package scfpack

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/yaml.v3"
)

const credentialNames = `(?:SECRET(?:_KEY)?|PASSWORD|TOKEN|PRIVATE_KEY|APP_KEYS?_JSON|APP_KEY|API_KEY|JWT|NKEY(?:_SEED)?|ACCESS_KEY)`

var (
	// 形如 MOOX_..._SECRET: xxx 或 MOOX_..._PASSWORD=xxx 的赋值（含带引号的键），只看键，值由 hasNonEmptyCredentialAssignment 判断。
	credentialAssignment = regexp.MustCompile(`(?im)(^|[^A-Za-z0-9_])["']?(?:MOOX_[A-Z0-9_]*` + credentialNames + `|TENCENTCLOUD_(?:SECRET_ID|SECRET_KEY|SESSION_TOKEN)|TENCENT_(?:SECRET_ID|SECRET_KEY|SESSION_TOKEN))["']?[ \t]*[:=][ \t]*`)
	// 环境变量列表里 name: 的取值。
	credentialEnvironmentName = regexp.MustCompile(`(?i)^(?:MOOX_[A-Z0-9_]*` + credentialNames + `|TENCENTCLOUD_(?:SECRET_ID|SECRET_KEY|SESSION_TOKEN)|TENCENT_(?:SECRET_ID|SECRET_KEY|SESSION_TOKEN))$`)
	natsCredential            = regexp.MustCompile(`(?i)-----BEGIN (?:NATS USER JWT|USER NKEY SEED)-----`)
)

// Normpath 返回路径的词法规范化结果（不访问文件系统，不解析符号链接），打包脚本据此拒绝经符号链接绕出去的路径。
func Normpath(path string) string { return filepath.Clean(path) }

// CheckPackage 检查包目录下的所有文件：不能有符号链接，除了 main 二进制只能有 .yaml/.yml，且每个配置都必须无凭据。
// 出错时返回的错误信息就是给用户看的原因（带相对路径）。
func CheckPackage(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return errors.New("SCF package symlinks are not permitted")
		}
		if !d.Type().IsRegular() || d.Name() == "main" {
			return nil
		}
		if ext := filepath.Ext(path); ext != ".yaml" && ext != ".yml" {
			return errors.New("unexpected SCF configuration payload")
		}
		content, err := os.ReadFile(path)
		if err == nil {
			err = CheckConfig(content)
		}
		if err != nil {
			rel, _ := filepath.Rel(root, path)
			return fmt.Errorf("SCF configuration must be valid and credential-free: %s", rel)
		}
		return nil
	})
}

// CheckConfig 检查一份配置的内容。
func CheckConfig(content []byte) error {
	text := string(content)
	if strings.Contains(strings.ToUpper(text), "PRIVATE KEY") {
		return errors.New("private key payload is not permitted")
	}
	if natsCredential.MatchString(text) {
		return errors.New("NATS credential material is not permitted")
	}
	if hasNonEmptyCredentialAssignment(text) {
		return errors.New("credential assignment is not permitted")
	}
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	for {
		var node yaml.Node
		if err := decoder.Decode(&node); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		if err := checkNode(&node); err != nil {
			return err
		}
		var document any
		if err := node.Decode(&document); err != nil {
			return err
		}
		if err := checkValue(document, ""); err != nil {
			return err
		}
	}
}

// checkNode 拒绝别名、锚点和重复键（它们能把同一个值藏在多处，或让后一个键悄悄覆盖前一个）。
func checkNode(node *yaml.Node) error {
	if node.Kind == yaml.AliasNode || node.Anchor != "" {
		return errors.New("configuration aliases are not permitted")
	}
	if node.Kind == yaml.MappingNode {
		seen := map[string]bool{}
		for i := 0; i+1 < len(node.Content); i += 2 {
			key := node.Content[i]
			id := key.ShortTag() + "\x00" + key.Value
			if seen[id] {
				return errors.New("duplicate configuration key")
			}
			seen[id] = true
		}
	}
	for _, child := range node.Content {
		if err := checkNode(child); err != nil {
			return err
		}
	}
	return nil
}

// hasNonEmptyCredentialAssignment 找出凭据键后面带了非空值的赋值；值为空、null、~ 或只有注释的占位写法放行。
func hasNonEmptyCredentialAssignment(content string) bool {
	for _, loc := range credentialAssignment.FindAllStringIndex(content, -1) {
		tail := content[loc[1]:]
		value := ""
		if tail != "" {
			value = strings.TrimSpace(firstLine(tail))
		}
		if value == "" || strings.HasPrefix(value, "#") {
			continue
		}
		if value[0] == '\'' || value[0] == '"' {
			closing := strings.IndexByte(value[1:], value[0])
			if closing < 0 {
				return true
			}
			closing++
			remainder := strings.TrimSpace(value[closing+1:])
			if strings.TrimSpace(value[1:closing]) != "" || remainder != "" && !strings.HasPrefix(remainder, "#") {
				return true
			}
			continue
		}
		fields := strings.Fields(value)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "~" || strings.EqualFold(fields[0], "null") {
			remainder := strings.TrimSpace(strings.TrimPrefix(value, fields[0]))
			if remainder != "" && !strings.HasPrefix(remainder, "#") {
				return true
			}
			continue
		}
		return true
	}
	return false
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}

func emptyValue(value any) bool {
	if value == nil {
		return true
	}
	text, ok := value.(string)
	return ok && strings.TrimSpace(text) == ""
}

// checkValue 递归检查解码后的值：敏感字段名下不能有非空值，环境变量列表里不能给凭据变量赋值，字符串里不能带私钥或 NATS 凭据。
func checkValue(value any, path string) error {
	switch v := value.(type) {
	case map[string]any:
		entries := make(map[string]any, len(v))
		for key, child := range v {
			entries[key] = child
		}
		return checkMapping(entries, path)
	case map[any]any:
		entries := make(map[string]any, len(v))
		for key, child := range v {
			entries[fmt.Sprint(key)] = child
		}
		return checkMapping(entries, path)
	case []any:
		for _, child := range v {
			if err := checkValue(child, path); err != nil {
				return err
			}
		}
	case string:
		if strings.Contains(strings.ToUpper(v), "PRIVATE KEY") {
			return errors.New("private key payload is not permitted")
		}
		if natsCredential.MatchString(v) {
			return errors.New("NATS credential material is not permitted")
		}
	}
	return nil
}

func checkMapping(entries map[string]any, path string) error {
	field := func(name string) (any, error) {
		var found []any
		for key, child := range entries {
			if strings.EqualFold(key, name) {
				found = append(found, child)
			}
		}
		if len(found) > 1 {
			return nil, errors.New("duplicate credential environment field")
		}
		if len(found) == 0 {
			return nil, nil
		}
		return found[0], nil
	}
	envName, err := field("name")
	if err != nil {
		return err
	}
	envValue, err := field("value")
	if err != nil {
		return err
	}
	if text, ok := envName.(string); ok && credentialEnvironmentName.MatchString(strings.TrimSpace(text)) && !emptyValue(envValue) {
		return errors.New("credential environment variable value is not permitted")
	}
	for key, child := range entries {
		name := strings.ReplaceAll(strings.ToLower(key), "-", "_")
		fieldPath := path + "." + key
		if sensitiveField(name, fieldPath) && !emptyValue(child) {
			return errors.New("nonempty credential field is not permitted")
		}
		if err := checkValue(child, fieldPath); err != nil {
			return err
		}
	}
	return nil
}

// sensitiveField 判断字段名是否是凭据；.system.service_auth.access_key 是服务鉴权的公开标识，不算。
func sensitiveField(name, fieldPath string) bool {
	switch name {
	case "app_key", "key", "token", "seed":
		return true
	}
	for _, part := range []string{"secret", "password", "private_key", "jwt", "nkey", "credential"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	for _, suffix := range []string{"api_key", "app_keys_json", "hmac_key_file", "_token", "_seed"} {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return strings.HasSuffix(name, "access_key") && fieldPath != ".system.service_auth.access_key"
}
