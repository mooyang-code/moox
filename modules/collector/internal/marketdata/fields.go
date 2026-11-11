package marketdata

import (
	"context"
	"strings"
	"time"
)

// FieldValueType 是附加字段的值类型。
type FieldValueType string

const (
	FieldTypeDouble FieldValueType = "double"
	FieldTypeInt    FieldValueType = "int"
)

// FieldValue 是一根 K 线上一个附加字段的值；按 Type 取 Double 或 Int。
type FieldValue struct {
	Type   FieldValueType
	Double float64
	Int    int64
}

// DoubleField 构造浮点值。
func DoubleField(value float64) FieldValue {
	return FieldValue{Type: FieldTypeDouble, Double: value}
}

// IntField 构造整数值。
func IntField(value int64) FieldValue {
	return FieldValue{Type: FieldTypeInt, Int: value}
}

// FieldSpec 描述一个附加字段：采集任务可以在输出字段里选它，默认不选。
// 分组、单位以 Storage 字段目录（config/setup/metadata.yaml）为准，这里不重复。
type FieldSpec struct {
	// ID 是输出字段名，同一市场内唯一，例如 adj_factor。
	ID string
	// Name 是结果数据集这一列的显示名。
	Name string
	Type FieldValueType
	// Fill 写进数据集列属性的 fill：prev 或 zero。
	Fill string
	// Frequencies 是适用的 K 线频率。
	Frequencies []string
}

// SupportsFrequency 判断字段是否适用于这个 K 线频率。
func (s FieldSpec) SupportsFrequency(frequency string) bool {
	want, err := ParseFrequency(frequency)
	if err != nil {
		return false
	}
	for _, candidate := range s.Frequencies {
		if parsed, err := ParseFrequency(candidate); err == nil && parsed == want {
			return true
		}
	}
	return false
}

// FieldRequest 是一次附加字段取数：一个标的、一段时间。
type FieldRequest struct {
	MarketID       MarketID
	InstrumentType InstrumentType
	SubjectID      string
	// ProviderSymbol 是这个标的在 K 线行情源上的代码。
	ProviderSymbol string
	Frequency      string
	// Fields 是本次要取的字段 ID（这个来源提供、且被写入目标选中的）。
	Fields []string
	// Start、End 是要写的 K 线覆盖的时间范围：[最早一根的开始, 最晚一根的收盘)。
	Start time.Time
	End   time.Time
	// Realtime 表示来自实时批次：来源可以读进程内缓存，限速时排在补采前面。
	Realtime bool
}

// FieldSource 是能给 K 线补充附加字段的来源才实现的可选能力。
// 只提供附加字段的来源用独立的提供方 ID 登记进行情源目录，不进路由配置。
type FieldSource interface {
	MarketProvider
	// FieldSpecs 返回这个实例提供的字段；同一类型的适配器服务的市场没有这些字段时（例如现货没有资金费率）返回空。
	FieldSpecs() []FieldSpec
	// FetchFields 返回 [Start, End) 内各根 K 线上各字段的值，键是 K 线的开始时刻。每个值都是这根 K 线收盘时已经能知道的最新值。
	// 来源确实没有值的根、字段不出现在结果里（照常写行，这一列留空）；取数失败返回错误，不能返回部分结果。
	FetchFields(ctx context.Context, req FieldRequest) (map[time.Time]map[string]FieldValue, error)
}

// FieldOffer 是目录里一个来源在某个（市场, 品种）下提供的一个附加字段。
type FieldOffer struct {
	MarketID   MarketID
	Instrument InstrumentType
	ProviderID string
	Spec       FieldSpec
}

// FieldOffers 是目录里全部可用的附加字段，供建任务校验、结果数据集建列、任务表单使用。
type FieldOffers []FieldOffer

// Find 返回（市场, 品种）下这个字段的来源。
func (o FieldOffers) Find(marketID, instrument, fieldID string) (FieldOffer, bool) {
	for _, offer := range o {
		if foldEqual(string(offer.MarketID), marketID) && foldEqual(string(offer.Instrument), instrument) && foldEqual(offer.Spec.ID, fieldID) {
			return offer, true
		}
	}
	return FieldOffer{}, false
}

// Spec 返回市场内这个字段的定义（不限品种）；不是附加字段时返回 false。
func (o FieldOffers) Spec(marketID, fieldID string) (FieldSpec, bool) {
	for _, offer := range o {
		if foldEqual(string(offer.MarketID), marketID) && foldEqual(offer.Spec.ID, fieldID) {
			return offer.Spec, true
		}
	}
	return FieldSpec{}, false
}

// Specs 返回市场内全部附加字段的定义（按登记顺序，同名只取第一个）。
func (o FieldOffers) Specs(marketID string) []FieldSpec {
	var specs []FieldSpec
	seen := map[string]struct{}{}
	for _, offer := range o {
		id := strings.ToLower(strings.TrimSpace(offer.Spec.ID))
		if _, dup := seen[id]; dup || !foldEqual(string(offer.MarketID), marketID) {
			continue
		}
		seen[id] = struct{}{}
		specs = append(specs, offer.Spec)
	}
	return specs
}

func foldEqual(left, right string) bool {
	return strings.EqualFold(strings.TrimSpace(left), strings.TrimSpace(right))
}

// FieldSources 构造（市场, 品种）下全部提供附加字段的来源；deps 给出每个登记项的依赖（运行参数按提供方 ID 加载）。
// 与 K 线的来源分配无关：K 线适配器自己提供的字段（资金费率）和只提供附加字段的来源都在这里。
func (c *Catalog) FieldSources(marketID MarketID, instrument InstrumentType, deps func(CatalogEntry) (Deps, error)) ([]FieldSource, error) {
	want := normalizeEntry(CatalogEntry{MarketID: marketID, Instrument: instrument})
	var out []FieldSource
	for _, entry := range c.entries {
		if entry.MarketID != want.MarketID || entry.Instrument != want.Instrument {
			continue
		}
		values, err := deps(entry)
		if err != nil {
			return nil, err
		}
		provider, err := entry.Build(values)
		if err != nil {
			return nil, err
		}
		if source, ok := provider.(FieldSource); ok && len(source.FieldSpecs()) > 0 {
			out = append(out, source)
		}
	}
	return out, nil
}

// FieldOffers 列出目录里全部可用的附加字段（用默认依赖构造实例，只读取字段定义，不发请求）。
func (c *Catalog) FieldOffers() (FieldOffers, error) {
	var offers FieldOffers
	for _, entry := range c.entries {
		provider, err := entry.Build(Deps{})
		if err != nil {
			return nil, err
		}
		source, ok := provider.(FieldSource)
		if !ok {
			continue
		}
		for _, spec := range source.FieldSpecs() {
			offers = append(offers, FieldOffer{MarketID: entry.MarketID, Instrument: entry.Instrument, ProviderID: entry.Key.ProviderID, Spec: spec})
		}
	}
	return offers, nil
}

// FindFieldSource 在一组来源里按字段 ID 找提供它的来源。
func FindFieldSource(sources []FieldSource, fieldID string) (FieldSource, bool) {
	for _, source := range sources {
		for _, spec := range source.FieldSpecs() {
			if foldEqual(spec.ID, fieldID) {
				return source, true
			}
		}
	}
	return nil, false
}
