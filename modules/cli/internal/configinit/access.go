package configinit

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/BurntSushi/toml"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
)

// MaskedValue 是密钥类配置在 config get 里的替身。
const MaskedValue = "***"

var secretKeyPattern = regexp.MustCompile(`(?i)(password|passphrase|secret|token|api_key|access_key|private_key)`)

// isSecretKey 判断键名是不是密钥类（路径类的 *_file、*_path 不算）。
func isSecretKey(key string) bool {
	k := strings.ToLower(key)
	if strings.HasSuffix(k, "_file") || strings.HasSuffix(k, "_path") {
		return false
	}
	return secretKeyPattern.MatchString(k)
}

// loadPlain 在本机读取 moox.toml；加密的先解开。
func loadPlain(path string, keys configcrypt.KeyStore) (raw, plain []byte, err error) {
	if keys == nil {
		keys = configcrypt.Default()
	}
	if raw, err = readConfigFile(path); err != nil {
		return nil, nil, err
	}
	if !configcrypt.IsEncrypted(raw) {
		return raw, raw, nil
	}
	if plain, err = configcrypt.Decrypt(raw, keys); err != nil {
		return nil, nil, fmt.Errorf("解不开 %s：%w", path, err)
	}
	return raw, plain, nil
}

// GetConfig 读取 moox.toml 里某个路径（点号分隔，数组用序号，空路径取整份）的值。
// 密钥类的键默认返回 "***"，reveal 为 true 才返回明文。
func GetConfig(path, target string, reveal bool, keys configcrypt.KeyStore) (any, error) {
	_, plain, err := loadPlain(path, keys)
	if err != nil {
		return nil, err
	}
	var doc map[string]any
	if _, err := toml.Decode(string(plain), &doc); err != nil {
		return nil, fmt.Errorf("moox.toml 不是合法的 TOML：%w", err)
	}
	var node any = doc
	target = strings.TrimSpace(target)
	if target != "" {
		for _, seg := range strings.Split(target, ".") {
			switch cur := node.(type) {
			case map[string]any:
				next, ok := cur[seg]
				if !ok {
					return nil, fmt.Errorf("moox.toml 里没有 %s（可用的键：%s）", target, strings.Join(sortedKeys(cur), "、"))
				}
				node = next
			case []map[string]any:
				i, err := strconv.Atoi(seg)
				if err != nil || i < 0 || i >= len(cur) {
					return nil, fmt.Errorf("%s 是数组（共 %d 项），要用 0～%d 的序号", target, len(cur), len(cur)-1)
				}
				node = cur[i]
			case []any:
				i, err := strconv.Atoi(seg)
				if err != nil || i < 0 || i >= len(cur) {
					return nil, fmt.Errorf("%s 是数组（共 %d 项），要用 0～%d 的序号", target, len(cur), len(cur)-1)
				}
				node = cur[i]
			default:
				return nil, fmt.Errorf("%s 是单个值，下面没有 %s", target, seg)
			}
		}
	}
	if reveal {
		return node, nil
	}
	last := target
	if i := strings.LastIndex(target, "."); i >= 0 {
		last = target[i+1:]
	}
	return maskSecrets(node, last), nil
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func maskSecrets(node any, key string) any {
	switch v := node.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, child := range v {
			out[k] = maskSecrets(child, k)
		}
		return out
	case []map[string]any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = maskSecrets(child, key)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = maskSecrets(child, key)
		}
		return out
	default:
		if isSecretKey(key) && fmt.Sprint(v) != "" {
			return MaskedValue
		}
		return node
	}
}

// SetConfigReport 是 config set 的结果；不含任何配置值。
type SetConfigReport struct {
	Path       string `json:"path"`
	Key        string `json:"key"`
	Changed    bool   `json:"changed"`
	Valid      bool   `json:"valid"`
	Written    bool   `json:"written"`
	Backup     string `json:"backup,omitempty"`
	NextAction string `json:"next_action,omitempty"`
}

// SetConfig 改 moox.toml 里一个已有的单值键。target 写成 表名.键（根部的键直接写键），
// 值按 true/false、整数、小数识别，其余按字符串。只改那一行，注释与其余内容原样保留；
// 改后按当前格式校验，通过且 write 为 true 才备份并重新加密写回。
func SetConfig(path, target, value string, write bool, keys configcrypt.KeyStore, now func() time.Time) (SetConfigReport, error) {
	report := SetConfigReport{Path: path, Key: target}
	if now == nil {
		now = time.Now
	}
	if keys == nil {
		keys = configcrypt.Default()
	}
	raw, plain, err := loadPlain(path, keys)
	if err != nil {
		return report, err
	}
	table, key := "", strings.TrimSpace(target)
	if i := strings.LastIndex(key, "."); i >= 0 {
		table, key = key[:i], key[i+1:]
	}
	if key == "" {
		return report, errors.New("config set 需要写成 表名.键=值，例如 admin.username=ops 或 factors.enabled=true")
	}
	edited, err := setScalarKey(plain, table, key, value)
	if err != nil {
		return report, err
	}
	report.Changed = !bytes.Equal(edited, plain)
	if _, err := setupconfig.Parse(edited); err != nil {
		return report, fmt.Errorf("改写后的配置校验没有通过，未写回：%w", err)
	}
	report.Valid = true
	if !write || !report.Changed {
		if report.Changed {
			report.NextAction = "确认无误后加 --yes 写回"
		}
		return report, nil
	}
	backup, err := sealAndReplace(path, raw, edited, keys, now)
	if err != nil {
		return report, err
	}
	report.Backup, report.Written = backup, true
	report.NextAction = "运行一次 moox-cli config backup 更新管理后台的备份"
	return report, nil
}

// sealAndReplace 备份原文件，把新内容加密后替换回去；写回前确认文件没被别人改过。
func sealAndReplace(path string, raw, edited []byte, keys configcrypt.KeyStore, now func() time.Time) (string, error) {
	current, err := readConfigFile(path)
	if err != nil {
		return "", err
	}
	if sha256.Sum256(current) != sha256.Sum256(raw) {
		return "", errors.New("moox.toml 在修改过程中被改动过，未写回；请重新运行")
	}
	backup := fmt.Sprintf("%s.bak-%s", path, now().Format("20060102150405"))
	if err := writeExclusive(backup, raw); err != nil {
		return "", err
	}
	sealed, err := configcrypt.Encrypt(edited, keys)
	if err != nil {
		return "", fmt.Errorf("加密失败：%w", err)
	}
	if err := replaceFile(path, sealed); err != nil {
		return "", err
	}
	return backup, nil
}

// setScalarKey 把 [table] 里已有的 key 的值换成 value（只换那一行，保留行尾注释）；
// 表或键不存在、表是 [[数组表]]、键的现值是数组时报错，不凭空创建。
func setScalarKey(plain []byte, table, key, value string) ([]byte, error) {
	lines := strings.Split(string(plain), "\n")
	blocks := splitBlocks(lines)
	found := false
	for bi := range blocks {
		b := &blocks[bi]
		if b.header != table {
			continue
		}
		if b.array {
			return nil, fmt.Errorf("[[%s]] 是数组表，config set 不能改；请用 config edit", table)
		}
		found = true
		for i := range b.lines {
			k, old, ok := keyValue(b.lines[i])
			if !ok || k != key {
				continue
			}
			if valueEnd(b.lines, i) != i || strings.HasPrefix(strings.TrimSpace(old), "[") || strings.HasPrefix(strings.TrimSpace(old), "{") {
				return nil, fmt.Errorf("%s 的现值是数组或多行值，config set 不能改；请用 config edit", joinPath(table, key))
			}
			b.lines[i] = b.lines[i][:strings.Index(b.lines[i], "=")] + "= " + tomlScalar(value) + trailingComment(old)
			var out []string
			for _, blk := range blocks {
				out = append(out, blk.lines...)
			}
			return []byte(strings.Join(out, "\n")), nil
		}
	}
	if !found {
		return nil, fmt.Errorf("moox.toml 里没有 [%s]；config set 只改已有的表", table)
	}
	return nil, fmt.Errorf("[%s] 里没有 %s；config set 只改已有的键", table, key)
}

func joinPath(table, key string) string {
	if table == "" {
		return key
	}
	return table + "." + key
}

var intPattern = regexp.MustCompile(`^-?[0-9]+$`)

// tomlScalar 把命令行上的值写成 TOML：true/false、整数、小数原样，其余加引号。
func tomlScalar(value string) string {
	value = strings.TrimSpace(value)
	if value == "true" || value == "false" || intPattern.MatchString(value) {
		return value
	}
	if _, err := strconv.ParseFloat(value, 64); err == nil && strings.Contains(value, ".") {
		return value
	}
	return strconv.Quote(value)
}

// trailingComment 取出值后面的行尾注释（含前导空格）；没有则为空。
func trailingComment(old string) string {
	old = strings.TrimSpace(old)
	if len(old) > 0 && (old[0] == '"' || old[0] == '\'') {
		if end := strings.IndexByte(old[1:], old[0]); end >= 0 {
			old = old[end+2:]
		}
		if i := strings.Index(old, "#"); i >= 0 {
			return "  " + strings.TrimSpace(old[i:])
		}
		return ""
	}
	if i := strings.Index(old, "#"); i >= 0 {
		return "  " + strings.TrimSpace(old[i:])
	}
	return ""
}
