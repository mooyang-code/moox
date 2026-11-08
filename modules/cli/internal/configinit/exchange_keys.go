package configinit

import (
	"fmt"
	"strings"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
)

// ExchangeKeyEntry 是录入页和「修改配置」表单里的一把交易所密钥，对应 moox.toml 的一条 [[exchange_keys]]。
// 字段名与配置里的键一致（api_key、secret_key、passphrase），config get 才会自动打码。
type ExchangeKeyEntry struct {
	Name        string `json:"name"`
	Exchange    string `json:"exchange"`
	Environment string `json:"environment"`
	APIKey      string `json:"api_key"`
	SecretKey   string `json:"secret_key"`
	Passphrase  string `json:"passphrase"`
	ReadOnly    bool   `json:"readonly"`
}

// exchangeKeyEntries 从配置读出交易所密钥，顺序与配置里一致。
func exchangeKeyEntries(m setupconfig.Manifest) []ExchangeKeyEntry {
	entries := make([]ExchangeKeyEntry, 0, len(m.ExchangeKeys))
	for _, key := range m.ExchangeKeys {
		entries = append(entries, ExchangeKeyEntry{
			Name: key.Name, Exchange: key.Exchange, Environment: key.Environment,
			APIKey: key.APIKey, SecretKey: key.SecretKey, Passphrase: key.Passphrase, ReadOnly: key.ReadOnly,
		})
	}
	return entries
}

// normalizeExchangeKeyEntries 整理页面提交的交易所密钥：去掉首尾空白，交易所与环境小写化，拒绝控制字符与超长。
// 语义校验（名称字符、重复、OKX 的 passphrase）交给 setupconfig.Parse；错误信息不含密钥值。
func normalizeExchangeKeyEntries(entries []ExchangeKeyEntry) ([]ExchangeKeyEntry, error) {
	out := make([]ExchangeKeyEntry, 0, len(entries))
	for i, entry := range entries {
		entry.Name = strings.TrimSpace(entry.Name)
		entry.Exchange = strings.ToLower(strings.TrimSpace(entry.Exchange))
		entry.Environment = strings.ToLower(strings.TrimSpace(entry.Environment))
		entry.APIKey = strings.TrimSpace(entry.APIKey)
		for _, field := range []struct{ name, value string }{
			{"名称", entry.Name}, {"交易所", entry.Exchange}, {"环境", entry.Environment},
			{"API Key", entry.APIKey}, {"Secret Key", entry.SecretKey}, {"Passphrase", entry.Passphrase},
		} {
			if err := checkText(fmt.Sprintf("第 %d 把交易所密钥的%s", i+1, field.name), field.value, false); err != nil {
				return nil, err
			}
		}
		out = append(out, entry)
	}
	return out, nil
}

// renderExchangeKeys 把交易所密钥写成 [[exchange_keys]] 数组段，每段后空一行。passphrase 与 readonly 没有值时不写。
func renderExchangeKeys(entries []ExchangeKeyEntry) []string {
	var out []string
	for _, entry := range entries {
		out = append(out,
			"[[exchange_keys]]",
			"name = "+tomlString(entry.Name),
			"exchange = "+tomlString(entry.Exchange),
			"environment = "+tomlString(entry.Environment),
			"api_key = "+tomlString(entry.APIKey),
			"secret_key = "+tomlString(entry.SecretKey),
		)
		if entry.Passphrase != "" {
			out = append(out, "passphrase = "+tomlString(entry.Passphrase))
		}
		if entry.ReadOnly {
			out = append(out, "readonly = true")
		}
		out = append(out, "")
	}
	return out
}

// replaceExchangeKeys 用 entries 整体替换配置里的全部 [[exchange_keys]]。
// tomledit 只认 [段]、不认数组段，所以整段的增删改按 splitBlocks 的块来做：旧块去掉，新块写在第一个旧块的位置（原来没有时写在文件末尾）。
// 紧跟在旧块后面的注释属于下一个段（splitBlocks 把它们算进上一个块），要原样留下，不能跟着一起删。
func replaceExchangeKeys(lines []string, entries []ExchangeKeyEntry) []string {
	blocks := splitBlocks(lines)
	var head, rest []string
	found := false
	for _, b := range blocks {
		if b.header != "exchange_keys" || !b.array {
			if found {
				rest = append(rest, b.lines...)
			} else {
				head = append(head, b.lines...)
			}
			continue
		}
		found = true
		if tail := trailingComments(b.lines); len(tail) > 0 {
			rest = append(rest, tail...)
		}
	}
	if !found && len(entries) == 0 {
		return lines
	}
	endsWithNewline := len(lines) > 0 && lines[len(lines)-1] == ""
	rendered := renderExchangeKeys(entries)
	if len(rendered) == 0 {
		out := joinSeparated(head, rest)
		return keepFinalNewline(out, endsWithNewline)
	}
	out := joinSeparated(head, rendered)
	out = joinSeparated(out, rest)
	return keepFinalNewline(out, true)
}

// trailingComments 取出块末尾属于下一个段的注释：最后一个键值行之后、从第一条注释开始的部分（含其间的空行）。
func trailingComments(lines []string) []string {
	last := len(lines) - 1
	for last >= 0 && (strings.TrimSpace(lines[last]) == "" || strings.HasPrefix(strings.TrimSpace(lines[last]), "#")) {
		last--
	}
	for i := last + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
			end := len(lines)
			for end > i && strings.TrimSpace(lines[end-1]) == "" {
				end--
			}
			return lines[i:end]
		}
	}
	return nil
}

// joinSeparated 把两段行接起来，接缝处恰好留一个空行（任一边为空时不加）。
func joinSeparated(first, second []string) []string {
	first = trimTrailingBlank(first)
	for len(second) > 0 && strings.TrimSpace(second[0]) == "" {
		second = second[1:]
	}
	if len(first) == 0 || len(second) == 0 {
		return append(append([]string(nil), first...), second...)
	}
	out := append(append([]string(nil), first...), "")
	return append(out, second...)
}

func trimTrailingBlank(lines []string) []string {
	end := len(lines)
	for end > 0 && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return lines[:end]
}

// keepFinalNewline 保证文件以单个换行结尾（lines 的最后一项是空串）。
func keepFinalNewline(lines []string, want bool) []string {
	lines = trimTrailingBlank(lines)
	if want {
		lines = append(lines, "")
	}
	return lines
}
