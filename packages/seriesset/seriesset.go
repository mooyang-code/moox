// Package seriesset 定义时间序列集合的规范键、排序、稠密序号与 hash。
// Collector 与 Storage 共用同一实现，保证两边对同一标的集合得到相同的 hash 与序号。
package seriesset

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Series 是一条时间序列的身份：标的加 series_tag，与行的身份一致；series_tag 由写入方决定，这里不解释。
type Series struct {
	SubjectID string `json:"subject_id"`
	SeriesTag string `json:"series_tag"`
}

// CanonicalKey 返回规范序列键：两项各自去空白、转小写，以 NUL 连接。
func CanonicalKey(subjectID, seriesTag string) string {
	parts := []string{subjectID, seriesTag}
	for i := range parts {
		parts[i] = strings.ToLower(strings.TrimSpace(parts[i]))
	}
	return strings.Join(parts, "\x00")
}

// Key 返回该序列的规范键。
func (s Series) Key() string {
	return CanonicalKey(s.SubjectID, s.SeriesTag)
}

// Hash 计算已排序规范键集合的 hash。
func Hash(sortedKeys []string) string {
	h := sha256.New()
	for _, key := range sortedKeys {
		_, _ = h.Write([]byte(key))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Set 是规范化后的序列集合：序号 i 对应 Series[i]。
type Set struct {
	Series []Series
	Keys   []string
	Hash   string
}

// Canonicalize 按规范键排序并去重（相同键保留第一个），生成稠密序号与 hash。
func Canonicalize(input []Series) (Set, error) {
	if len(input) == 0 {
		return Set{}, errors.New("series set is empty")
	}
	byKey := make(map[string]Series, len(input))
	for _, item := range input {
		if strings.TrimSpace(item.SubjectID) == "" {
			return Set{}, errors.New("series subject_id is required")
		}
		key := item.Key()
		if _, exists := byKey[key]; !exists {
			byKey[key] = item
		}
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := Set{Series: make([]Series, len(keys)), Keys: keys}
	for i, key := range keys {
		out.Series[i] = byKey[key]
	}
	out.Hash = Hash(keys)
	return out, nil
}

// VerifySorted 检查输入已按规范键严格升序且无重复，返回其 hash。
func VerifySorted(input []Series) (string, error) {
	if len(input) == 0 {
		return "", errors.New("series set is empty")
	}
	keys := make([]string, len(input))
	for i, item := range input {
		if strings.TrimSpace(item.SubjectID) == "" {
			return "", fmt.Errorf("series %d subject_id is required", i)
		}
		keys[i] = item.Key()
		if i > 0 && keys[i] <= keys[i-1] {
			return "", fmt.Errorf("series %d is not in strictly ascending canonical order", i)
		}
	}
	return Hash(keys), nil
}
