package marketmanifest

import (
	"fmt"
	"sort"
	"strings"
)

type SourceRef struct {
	ProviderID      string   `json:"provider_id"`
	SourceID        string   `json:"source_id"`
	ProtocolVariant string   `json:"protocol_variant"`
	Transport       string   `json:"transport"`
	Port            int      `json:"port"`
	Frequencies     []string `json:"frequencies"`
}

type Manifest struct {
	MarketID       string      `json:"market_id"`
	InstrumentType string      `json:"instrument_type"`
	DatasetID      string      `json:"dataset_id"`
	CalendarID     string      `json:"calendar_id"`
	Timezone       string      `json:"timezone"`
	Frequencies    []string    `json:"frequencies"`
	Sources        []SourceRef `json:"sources"`
	Enabled        bool        `json:"enabled"`
}

func (manifest Manifest) Validate() error {
	for name, value := range map[string]string{"market_id": manifest.MarketID, "instrument_type": manifest.InstrumentType, "dataset_id": manifest.DatasetID, "timezone": manifest.Timezone} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if len(manifest.Frequencies) == 0 {
		return fmt.Errorf("frequencies are required")
	}
	seenFreq := make(map[string]struct{}, len(manifest.Frequencies))
	for _, frequency := range manifest.Frequencies {
		frequency = strings.TrimSpace(frequency)
		if frequency == "" {
			return fmt.Errorf("frequency must not be empty")
		}
		if _, exists := seenFreq[frequency]; exists {
			return fmt.Errorf("duplicate frequency %q", frequency)
		}
		seenFreq[frequency] = struct{}{}
	}
	if len(manifest.Sources) == 0 {
		return fmt.Errorf("at least one source is required")
	}
	seenSource := make(map[string]struct{}, len(manifest.Sources))
	for index, source := range manifest.Sources {
		if strings.TrimSpace(source.ProviderID) == "" || strings.TrimSpace(source.SourceID) == "" {
			return fmt.Errorf("sources[%d] provider_id/source_id are required", index)
		}
		if strings.TrimSpace(source.ProtocolVariant) == "" || strings.TrimSpace(source.Transport) == "" {
			return fmt.Errorf("sources[%d] protocol_variant/transport are required", index)
		}
		if source.Port < 1 || source.Port > 65535 {
			return fmt.Errorf("sources[%d] port must be between 1 and 65535", index)
		}
		if len(source.Frequencies) == 0 {
			return fmt.Errorf("sources[%d] frequencies are required", index)
		}
		key := strings.ToLower(strings.TrimSpace(source.ProviderID) + "/" + strings.TrimSpace(source.SourceID))
		if _, exists := seenSource[key]; exists {
			return fmt.Errorf("duplicate source %q", key)
		}
		seenSource[key] = struct{}{}
		seenSourceFreq := make(map[string]struct{}, len(source.Frequencies))
		for _, frequency := range source.Frequencies {
			frequency = strings.TrimSpace(frequency)
			if frequency == "" {
				return fmt.Errorf("source %q frequency must not be empty", key)
			}
			if _, exists := seenSourceFreq[frequency]; exists {
				return fmt.Errorf("source %q has duplicate frequency %q", key, frequency)
			}
			seenSourceFreq[frequency] = struct{}{}
			if _, exists := seenFreq[frequency]; !exists {
				return fmt.Errorf("source %q frequency %q is not declared by manifest", key, frequency)
			}
		}
	}
	return nil
}

type Catalog struct {
	items map[string]Manifest
}

func NewCatalog(manifests ...Manifest) (*Catalog, error) {
	catalog := &Catalog{items: make(map[string]Manifest, len(manifests))}
	for _, manifest := range manifests {
		if err := manifest.Validate(); err != nil {
			return nil, err
		}
		key := strings.TrimSpace(manifest.MarketID) + "/" + strings.TrimSpace(manifest.InstrumentType)

		if _, exists := catalog.items[key]; exists {
			return nil, fmt.Errorf("duplicate market manifest %q", key)
		}
		manifest = cloneManifest(manifest)
		catalog.items[key] = manifest
	}
	return catalog, nil
}

func (catalog *Catalog) Lookup(marketID, instrumentType string) (Manifest, bool) {
	if catalog == nil {
		return Manifest{}, false
	}
	manifest, ok := catalog.items[strings.TrimSpace(marketID)+"/"+strings.TrimSpace(instrumentType)]
	if !ok {
		return Manifest{}, false
	}
	return cloneManifest(manifest), true
}

func cloneManifest(manifest Manifest) Manifest {
	manifest.Frequencies = append([]string(nil), manifest.Frequencies...)
	manifest.Sources = append([]SourceRef(nil), manifest.Sources...)
	for index := range manifest.Sources {
		manifest.Sources[index].Frequencies = append([]string(nil), manifest.Sources[index].Frequencies...)
	}
	return manifest
}

func (catalog *Catalog) List() []Manifest {
	if catalog == nil {
		return nil
	}
	keys := make([]string, 0, len(catalog.items))
	for key := range catalog.items {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]Manifest, 0, len(keys))
	for _, key := range keys {
		manifest, _ := catalog.Lookup(strings.SplitN(key, "/", 2)[0], strings.SplitN(key, "/", 2)[1])
		result = append(result, manifest)
	}
	return result
}

func DefaultCatalog() (*Catalog, error) {
	return NewCatalog(
		Manifest{MarketID: "stock_cn", InstrumentType: "equity", DatasetID: "stock_cn_kline", CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Frequencies: []string{"1m", "1d", "1w", "1M"}, Sources: []SourceRef{{ProviderID: "tdx", SourceID: "normal_7709", ProtocolVariant: "tdx_normal", Transport: "tcp", Port: 7709, Frequencies: []string{"1m", "1d", "1w", "1M"}}, {ProviderID: "eastmoney", SourceID: "stock_cn_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1m", "1d", "1w", "1M"}}, {ProviderID: "tencent", SourceID: "stock_cn_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1d"}}}, Enabled: true},
		Manifest{MarketID: "stock_cn", InstrumentType: "index", DatasetID: "stock_cn_index_kline", CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Frequencies: []string{"1m", "1d", "1w", "1M"}, Sources: []SourceRef{{ProviderID: "tdx", SourceID: "normal_7709", ProtocolVariant: "tdx_normal", Transport: "tcp", Port: 7709, Frequencies: []string{"1m", "1d", "1w", "1M"}}, {ProviderID: "eastmoney", SourceID: "index_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1m", "1d", "1w", "1M"}}}, Enabled: true},
		Manifest{MarketID: "stock_cn", InstrumentType: "convertible_bond", DatasetID: "stock_cn_convertible_bond_kline", CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Frequencies: []string{"1m", "1d"}, Sources: []SourceRef{{ProviderID: "tdx", SourceID: "normal_7709", ProtocolVariant: "tdx_normal", Transport: "tcp", Port: 7709, Frequencies: []string{"1m", "1d"}}, {ProviderID: "eastmoney", SourceID: "convertible_bond_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1m", "1d"}}}, Enabled: true},
		Manifest{MarketID: "stock_hk", InstrumentType: "equity", DatasetID: "stock_hk_kline", Timezone: "Asia/Hong_Kong", Frequencies: []string{"1m", "1d", "1w", "1M"}, Sources: []SourceRef{{ProviderID: "eastmoney", SourceID: "stock_hk_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1m", "1d", "1w", "1M"}}}, Enabled: true},
		Manifest{MarketID: "stock_us", InstrumentType: "equity", DatasetID: "stock_us_kline", Timezone: "America/New_York", Frequencies: []string{"1m", "1d", "1w", "1M"}, Sources: []SourceRef{{ProviderID: "eastmoney", SourceID: "stock_us_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1m", "1d", "1w", "1M"}}}, Enabled: true},
	)
}
