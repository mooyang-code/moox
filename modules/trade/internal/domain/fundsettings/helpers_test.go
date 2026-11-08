package fundsettings

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/trade/internal/domain/shared"
)

// parseForWrite 模拟写入路径对 JSON 原文的严格解析（拒绝未知键和大小写不规范的键），再走 ParseDocumentForWrite 校验。
func parseForWrite(raw, settlementAsset string) (Settings, string, error) {
	var doc Document
	if err := shared.DecodeStrictObject(raw, &doc); err != nil {
		return Settings{}, "", fmt.Errorf("资金设置：%w", err)
	}
	return ParseDocumentForWrite(doc, settlementAsset)
}

// parseStrict 按读取路径的大小写宽松键名解析成文档再校验（不带结算币规则），用来和宽松解析对照。
func parseStrict(raw string) (Settings, error) {
	var doc Document
	if strings.TrimSpace(raw) != "" {
		if err := json.Unmarshal([]byte(raw), &doc); err != nil {
			return Settings{}, err
		}
	}
	settings, _, err := ParseDocumentForWrite(doc, "")
	return settings, err
}
