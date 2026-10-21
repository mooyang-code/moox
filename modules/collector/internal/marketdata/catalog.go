package marketdata

import (
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/httpclient"
)

// SourceSettings 是数据源自身的运行参数（A 股按路由与数据源配置文件给出），没有配置的数据源取零值用默认。
type SourceSettings struct {
	BaseURL           string
	KlineEndpoint     string
	Hosts             []string
	Port              int
	MaxBarsPerRequest int
	RateLimit         RateLimitPolicy
}

// Deps 是构造数据源时由使用方注入的依赖：Collector（标的同步）、SCF 与本地执行端（取数）、
// collector-stream（接收）各自给出自己的 HTTP 客户端与配置，数据源的构造只在目录里写一处。
type Deps struct {
	HTTPClient *httpclient.HTTPClient
	// SourceID 为空时取登记的 source_id；A 股按路由配置给出。
	SourceID string
	Settings SourceSettings
}

// Factory 构造一个数据源实例。
type Factory func(Deps) (MarketProvider, error)

// CatalogEntry 是目录里的一个数据源：一个（市场, 品种, provider, source_id）对应一个工厂。
// 能力由实例实现的接口决定：InstrumentFetcher 用于标的同步，KlineFetcher 用于取数与补采，
// stream.Provider 用于 WS 接收，不另写能力清单。
type CatalogEntry struct {
	Key        SourceKey
	MarketID   MarketID
	Instrument InstrumentType
	// RouteSource 表示 source_id 由路由配置决定（A 股），查找时只按 provider 匹配。
	RouteSource bool
	New         Factory
}

// Catalog 是全部数据源的登记表。
type Catalog struct {
	entries []CatalogEntry
}

func normalizeEntry(entry CatalogEntry) CatalogEntry {
	entry.Key.ProviderID = strings.ToLower(strings.TrimSpace(entry.Key.ProviderID))
	entry.Key.SourceID = strings.ToLower(strings.TrimSpace(entry.Key.SourceID))
	entry.MarketID = MarketID(strings.ToLower(strings.TrimSpace(string(entry.MarketID))))
	entry.Instrument = InstrumentType(strings.ToLower(strings.TrimSpace(string(entry.Instrument))))
	return entry
}

// Register 登记一个数据源；同一（市场, 品种, provider, source_id）只能登记一次。
func (c *Catalog) Register(entry CatalogEntry) error {
	entry = normalizeEntry(entry)
	if err := entry.Key.Validate(); err != nil {
		return err
	}
	if entry.MarketID == "" || entry.Instrument == "" || entry.New == nil {
		return fmt.Errorf("%w: catalog entry %s requires market, instrument and factory", ErrInvalidRequest, entry.Key)
	}
	for _, existing := range c.entries {
		if existing.Key == entry.Key && existing.MarketID == entry.MarketID && existing.Instrument == entry.Instrument {
			return fmt.Errorf("%w: %s %s/%s", ErrProviderAlreadyRegistered, entry.Key, entry.MarketID, entry.Instrument)
		}
	}
	c.entries = append(c.entries, entry)
	return nil
}

// MustRegister 登记一组数据源，失败即 panic（只用于进程内置的目录）。
func (c *Catalog) MustRegister(entries ...CatalogEntry) *Catalog {
	for _, entry := range entries {
		if err := c.Register(entry); err != nil {
			panic(err)
		}
	}
	return c
}

// Entries 返回登记的全部数据源（按登记顺序）。
func (c *Catalog) Entries() []CatalogEntry {
	return append([]CatalogEntry(nil), c.entries...)
}

// Lookup 按（市场, 品种, provider, source_id）找登记项；sourceID 为空时匹配登记的默认 source_id。
func (c *Catalog) Lookup(marketID MarketID, instrument InstrumentType, providerID, sourceID string) (CatalogEntry, error) {
	want := normalizeEntry(CatalogEntry{Key: SourceKey{ProviderID: providerID, SourceID: sourceID}, MarketID: marketID, Instrument: instrument})
	for _, entry := range c.entries {
		if entry.MarketID != want.MarketID || entry.Instrument != want.Instrument || entry.Key.ProviderID != want.Key.ProviderID {
			continue
		}
		if entry.RouteSource || want.Key.SourceID == "" || entry.Key.SourceID == want.Key.SourceID {
			return entry, nil
		}
	}
	return CatalogEntry{}, fmt.Errorf("%w: %s/%s %s/%s", ErrProviderNotFound, want.MarketID, want.Instrument, want.Key.ProviderID, want.Key.SourceID)
}

// Has 报告目录里是否有这个市场与品种的数据源。
func (c *Catalog) Has(marketID MarketID, instrument InstrumentType) bool {
	want := normalizeEntry(CatalogEntry{MarketID: marketID, Instrument: instrument})
	for _, entry := range c.entries {
		if entry.MarketID == want.MarketID && entry.Instrument == want.Instrument {
			return true
		}
	}
	return false
}

// Provider 找到登记项并构造数据源实例。
func (c *Catalog) Provider(marketID MarketID, instrument InstrumentType, providerID, sourceID string, deps Deps) (MarketProvider, error) {
	entry, err := c.Lookup(marketID, instrument, providerID, sourceID)
	if err != nil {
		return nil, err
	}
	return entry.Build(deps)
}

// Build 用依赖构造这个数据源；Deps.SourceID 为空时取登记的 source_id。
func (e CatalogEntry) Build(deps Deps) (MarketProvider, error) {
	if strings.TrimSpace(deps.SourceID) == "" {
		deps.SourceID = e.Key.SourceID
	}
	provider, err := e.New(deps)
	if err != nil {
		return nil, fmt.Errorf("build %s: %w", e.Key, err)
	}
	return provider, nil
}

// InstrumentSource 是能提供标的快照的一个数据源。
type InstrumentSource struct {
	Entry   CatalogEntry
	Fetcher InstrumentFetcher
}

// Instruments 构造全部实现了 InstrumentFetcher 的数据源；deps 返回 false 的登记项跳过（例如路由里未启用）。
func (c *Catalog) Instruments(deps func(CatalogEntry) (Deps, bool)) ([]InstrumentSource, error) {
	var out []InstrumentSource
	for _, entry := range c.entries {
		values, ok := deps(entry)
		if !ok {
			continue
		}
		provider, err := entry.Build(values)
		if err != nil {
			return nil, err
		}
		if fetcher, ok := provider.(InstrumentFetcher); ok {
			out = append(out, InstrumentSource{Entry: entry, Fetcher: fetcher})
		}
	}
	return out, nil
}
