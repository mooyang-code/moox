package service

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// 任务用途：决定分区目录 <space>/<purpose>/<dir_name>/ 的第二级。
const (
	PurposeCollect = "collect"
	PurposeFactor  = "factor"
	PurposeData    = "data"
)

// 数据集属性（Storage 的 attributes）。
const (
	attrDatasetRole     = "dataset_role"
	attrSourceDatasetID = "source_dataset_id"
	attrMarketType      = "market_type"
	attrInstrumentType  = "instrument_type"

	roleRawCollection = "raw_collection"
	roleFactorResult  = "factor_result"
)

const maxDirNameLen = 64

var (
	dirNamePattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	nonDirChars    = regexp.MustCompile(`[^a-z0-9]+`)
)

// varietyAliases 是目录名里品种词的对照表：标签命中它时用表里的值；市场类型、品种类型也先查它。以后按需加。
var varietyAliases = map[string]string{
	"cn_hs300":         "hs300",
	"convertible_bond": "bond",
}

// purposeOf 按数据集角色给出用途：采集结果 collect、因子结果 factor，其余 data。
func purposeOf(ds Dataset) string {
	switch strings.TrimSpace(ds.Attributes[attrDatasetRole]) {
	case roleRawCollection:
		return PurposeCollect
	case roleFactorResult:
		return PurposeFactor
	default:
		return PurposeData
	}
}

// validateDirName 校验目录名：小写字母、数字与单个连字符，最长 64 个字符。
func validateDirName(name string) error {
	if len(name) > maxDirNameLen || !dirNamePattern.MatchString(name) {
		return fmt.Errorf("目录名 %q 不合规：只能用小写字母、数字和连字符（不能以连字符开头或结尾，也不能连续），最长 %d 个字符", name, maxDirNameLen)
	}
	return nil
}

// dirWord 把一个属性值转成目录名片段：小写，连续的非字母数字换成一个连字符；转换后为空时返回空。
func dirWord(value string) string {
	return strings.Trim(nonDirChars.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-"), "-")
}

// varietyWord 返回市场类型或品种类型对应的品种词：对照表里有就用表里的值。
func varietyWord(value string) string {
	key := strings.ToLower(strings.TrimSpace(value))
	if alias, ok := varietyAliases[key]; ok {
		return alias
	}
	return dirWord(key)
}

// suggestDirName 按“交易所 + 品种 + 类型 + 频率”拼出建议的目录名。
// source 是决定交易所与品种的数据集（因子结果为其源数据集）；allDay 表示空间全天交易（mgr 配置的 local_close 为空，即加密货币），
// 只有这种空间写交易所（data_source_id），其他空间的 data_source_id 是 API 来源，不写。
// 品种依次取：source 的标签命中对照表的值、market_type、instrument_type；类型只有采集结果写 kline；频率取本数据集的。
// 品种或频率拼不出来时回退为数据集 ID 去掉 dataset_ 前缀、下划线换成连字符。
func suggestDirName(ds, source Dataset, allDay bool) string {
	var parts []string
	if allDay {
		if exchange := dirWord(source.DataSourceID); exchange != "" {
			parts = append(parts, exchange)
		}
	}
	variety := ""
	for _, tag := range source.SubjectTags {
		if alias, ok := varietyAliases[strings.TrimSpace(tag)]; ok {
			variety = alias
			break
		}
	}
	if variety == "" {
		variety = varietyWord(source.Attributes[attrMarketType])
	}
	if variety == "" {
		variety = varietyWord(source.Attributes[attrInstrumentType])
	}
	// 频率按原样使用：规范写法都是小写（月为 1mo），大小写不合规的不去猜。
	if variety == "" || !dirNamePattern.MatchString(ds.Freq) {
		return fallbackDirName(ds.DatasetID)
	}
	parts = append(parts, variety)
	if purposeOf(ds) == PurposeCollect {
		parts = append(parts, "kline")
	}
	parts = append(parts, ds.Freq)
	name := strings.Join(parts, "-")
	if validateDirName(name) != nil {
		return fallbackDirName(ds.DatasetID)
	}
	return name
}

// fallbackDirName 是拼不出建议目录名时的回退：数据集 ID 去掉 dataset_ 前缀，下划线换成连字符。
func fallbackDirName(datasetID string) string {
	name := dirWord(strings.TrimPrefix(strings.TrimSpace(datasetID), "dataset_"))
	if len(name) > maxDirNameLen {
		name = strings.TrimRight(name[:maxDirNameLen], "-")
	}
	return name
}

// dirNameSource 返回决定交易所与品种的数据集：因子结果自己没有 market_type，取 source_dataset_id 指向的源数据集；其余取本数据集。
// 源数据集已不存在时按本数据集拼（拼不出来就回退）；读取失败直接报错，不能因为一时读不到就把回退名永久定下来。
func (s *Service) dirNameSource(ctx context.Context, spaceID string, ds Dataset) (Dataset, error) {
	sourceID := strings.TrimSpace(ds.Attributes[attrSourceDatasetID])
	if purposeOf(ds) != PurposeFactor || sourceID == "" {
		return ds, nil
	}
	source, err := s.datasets.GetDataset(ctx, spaceID, sourceID)
	if errors.Is(err, ErrDatasetNotFound) {
		return ds, nil
	}
	if err != nil {
		return Dataset{}, fmt.Errorf("读取因子结果的源数据集 %s 失败: %w", sourceID, err)
	}
	return source, nil
}

// suggestedDirName 返回数据集的建议目录名。
func (s *Service) suggestedDirName(ctx context.Context, spaceID string, ds Dataset) (string, error) {
	source, err := s.dirNameSource(ctx, spaceID, ds)
	if err != nil {
		return "", err
	}
	return suggestDirName(ds, source, s.cfg.Space(spaceID).LocalClose == ""), nil
}
