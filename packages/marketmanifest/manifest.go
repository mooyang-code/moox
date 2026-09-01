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
	Status          string   `json:"status"`
}

const (
	SourceEnabled     = "enabled"
	SourceShadow      = "shadow"
	SourceCatalogOnly = "catalog_only"
)

func (source SourceRef) IsEnabled() bool {
	status := strings.TrimSpace(source.Status)
	return status == "" || status == SourceEnabled
}

type Manifest struct {
	MarketID       string   `json:"market_id"`
	InstrumentType string   `json:"instrument_type"`
	DatasetID      string   `json:"dataset_id"`
	DatasetIDs     []string `json:"dataset_ids,omitempty"`
	// DatasetFrequencies prevents a multi-dataset manifest from accepting a
	// frequency that belongs to a different dataset.
	DatasetFrequencies map[string][]string `json:"dataset_frequencies,omitempty"`
	CalendarID         string              `json:"calendar_id"`
	Timezone           string              `json:"timezone"`
	Frequencies        []string            `json:"frequencies"`
	Sources            []SourceRef         `json:"sources"`
	Enabled            bool                `json:"enabled"`
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
	seenDataset := map[string]struct{}{strings.ToLower(strings.TrimSpace(manifest.DatasetID)): {}}
	for index, datasetID := range manifest.DatasetIDs {
		datasetID = strings.TrimSpace(datasetID)
		if datasetID == "" {
			return fmt.Errorf("dataset_ids[%d] must not be empty", index)
		}
		key := strings.ToLower(datasetID)
		if _, exists := seenDataset[key]; exists {
			return fmt.Errorf("duplicate dataset %q", datasetID)
		}
		seenDataset[key] = struct{}{}
	}
	if len(manifest.DatasetFrequencies) > 0 {
		for datasetID, frequencies := range manifest.DatasetFrequencies {
			datasetKey := strings.ToLower(strings.TrimSpace(datasetID))
			if _, exists := seenDataset[datasetKey]; !exists {
				return fmt.Errorf("dataset_frequencies contains unknown dataset %q", datasetID)
			}
			if len(frequencies) == 0 {
				return fmt.Errorf("dataset_frequencies[%q] must not be empty", datasetID)
			}
			seenDatasetFreq := make(map[string]struct{}, len(frequencies))
			for _, frequency := range frequencies {
				frequency = strings.TrimSpace(frequency)
				if frequency == "" {
					return fmt.Errorf("dataset_frequencies[%q] contains an empty frequency", datasetID)
				}
				if _, exists := seenFreq[frequency]; !exists {
					return fmt.Errorf("dataset_frequencies[%q] frequency %q is not declared by manifest", datasetID, frequency)
				}
				if _, exists := seenDatasetFreq[frequency]; exists {
					return fmt.Errorf("dataset_frequencies[%q] has duplicate frequency %q", datasetID, frequency)
				}
				seenDatasetFreq[frequency] = struct{}{}
			}
		}
		for datasetID := range seenDataset {
			if _, exists := manifest.DatasetFrequencies[datasetID]; exists {
				continue
			}
			found := false
			for candidate := range manifest.DatasetFrequencies {
				if strings.EqualFold(strings.TrimSpace(candidate), datasetID) {
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("dataset_frequencies is missing dataset %q", datasetID)
			}
		}
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
		switch strings.TrimSpace(source.Status) {
		case "", SourceEnabled, SourceShadow, SourceCatalogOnly:
		default:
			return fmt.Errorf("sources[%d] has invalid status %q", index, source.Status)
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
	manifest.DatasetIDs = append([]string(nil), manifest.DatasetIDs...)
	manifest.Frequencies = append([]string(nil), manifest.Frequencies...)
	if manifest.DatasetFrequencies != nil {
		datasetFrequencies := manifest.DatasetFrequencies
		manifest.DatasetFrequencies = make(map[string][]string, len(datasetFrequencies))
		for datasetID, frequencies := range datasetFrequencies {
			manifest.DatasetFrequencies[datasetID] = append([]string(nil), frequencies...)
		}
	}
	manifest.Sources = append([]SourceRef(nil), manifest.Sources...)
	for index := range manifest.Sources {
		manifest.Sources[index].Frequencies = append([]string(nil), manifest.Sources[index].Frequencies...)
	}
	return manifest
}

// SupportsDataset reports whether the dataset belongs to this market and
// instrument manifest. A market can expose more than one canonical dataset,
// such as Binance 1m bars and an existing 1h aggregate dataset.
func (manifest Manifest) SupportsDataset(datasetID string) bool {
	datasetID = strings.TrimSpace(datasetID)
	if datasetID == "" {
		return false
	}
	if strings.EqualFold(manifest.DatasetID, datasetID) {
		return true
	}
	for _, candidate := range manifest.DatasetIDs {
		if strings.EqualFold(candidate, datasetID) {
			return true
		}
	}
	return false
}

// SupportsDatasetFrequency reports whether the dataset/frequency pair is
// declared. Manifests without a DatasetFrequencies map retain the simple
// single-dataset behavior.
func (manifest Manifest) SupportsDatasetFrequency(datasetID, frequency string) bool {
	if !manifest.SupportsDataset(datasetID) {
		return false
	}
	if len(manifest.DatasetFrequencies) == 0 {
		return containsFrequency(manifest.Frequencies, frequency)
	}
	for candidate, frequencies := range manifest.DatasetFrequencies {
		if strings.EqualFold(strings.TrimSpace(candidate), strings.TrimSpace(datasetID)) {
			return containsFrequency(frequencies, frequency)
		}
	}
	return false
}

func containsFrequency(values []string, target string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == strings.TrimSpace(target) {
			return true
		}
	}
	return false
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
		Manifest{MarketID: "stock_cn", InstrumentType: "equity", DatasetID: "stock_cn_kline", CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Frequencies: []string{"1m", "1d", "1w", "1M"}, Sources: []SourceRef{{ProviderID: "tdx", SourceID: "normal_7709", ProtocolVariant: "tdx_normal", Transport: "tcp", Port: 7709, Frequencies: []string{"1m", "1d", "1w", "1M"}}, {ProviderID: "eastmoney", SourceID: "stock_cn_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1m", "1d", "1w", "1M"}}, {ProviderID: "tencent", SourceID: "stock_cn_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1d"}}, {ProviderID: "ths", SourceID: "daily_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1d"}, Status: SourceCatalogOnly}, {ProviderID: "sina", SourceID: "stock_cn_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1d"}, Status: SourceCatalogOnly}, {ProviderID: "sina", SourceID: "stock_cn_minute_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1m"}, Status: SourceCatalogOnly}}, Enabled: true},
		Manifest{MarketID: "stock_cn", InstrumentType: "index", DatasetID: "stock_cn_index_kline", CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Frequencies: []string{"1m", "1d", "1w", "1M"}, Sources: []SourceRef{{ProviderID: "tdx", SourceID: "normal_7709", ProtocolVariant: "tdx_normal", Transport: "tcp", Port: 7709, Frequencies: []string{"1m", "1d", "1w", "1M"}}, {ProviderID: "eastmoney", SourceID: "index_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1m", "1d", "1w", "1M"}}, {ProviderID: "cni", SourceID: "index_cni_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1d"}, Status: SourceCatalogOnly}, {ProviderID: "csindex", SourceID: "index_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1d"}, Status: SourceCatalogOnly}, {ProviderID: "sw", SourceID: "index_sw_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1d", "1w", "1M"}, Status: SourceCatalogOnly}}, Enabled: true},
		Manifest{MarketID: "stock_cn", InstrumentType: "convertible_bond", DatasetID: "stock_cn_convertible_bond_kline", CalendarID: "cn_stock", Timezone: "Asia/Shanghai", Frequencies: []string{"1m", "1d"}, Sources: []SourceRef{{ProviderID: "tdx", SourceID: "normal_7709", ProtocolVariant: "tdx_normal", Transport: "tcp", Port: 7709, Frequencies: []string{"1m", "1d"}}, {ProviderID: "eastmoney", SourceID: "convertible_bond_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1m", "1d"}}}, Enabled: true},
		Manifest{MarketID: "stock_hk", InstrumentType: "equity", DatasetID: "stock_hk_kline", Timezone: "Asia/Hong_Kong", Frequencies: []string{"1m", "1d", "1w", "1M"}, Sources: []SourceRef{{ProviderID: "eastmoney", SourceID: "stock_hk_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1m", "1d", "1w", "1M"}}, {ProviderID: "sina", SourceID: "stock_hk_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1d"}, Status: SourceCatalogOnly}, {ProviderID: "tdx", SourceID: "ex_classic_7727", ProtocolVariant: "tdx_ex_classic", Transport: "tcp", Port: 7727, Frequencies: []string{"1m", "1d", "1w", "1M"}, Status: SourceCatalogOnly}, {ProviderID: "tdx", SourceID: "ex_mac_7727", ProtocolVariant: "tdx_ex_mac", Transport: "tcp", Port: 7727, Frequencies: []string{"1m", "1d", "1w", "1M"}, Status: SourceCatalogOnly}}, Enabled: true},
		Manifest{MarketID: "stock_us", InstrumentType: "equity", DatasetID: "stock_us_kline", Timezone: "America/New_York", Frequencies: []string{"1m", "1d", "1w", "1M"}, Sources: []SourceRef{{ProviderID: "eastmoney", SourceID: "stock_us_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1m", "1d", "1w", "1M"}}, {ProviderID: "sina", SourceID: "stock_us_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1d"}, Status: SourceCatalogOnly}, {ProviderID: "tdx", SourceID: "ex_classic_7727", ProtocolVariant: "tdx_ex_classic", Transport: "tcp", Port: 7727, Frequencies: []string{"1m", "1d", "1w", "1M"}, Status: SourceCatalogOnly}, {ProviderID: "tdx", SourceID: "ex_mac_7727", ProtocolVariant: "tdx_ex_mac", Transport: "tcp", Port: 7727, Frequencies: []string{"1m", "1d", "1w", "1M"}, Status: SourceCatalogOnly}}, Enabled: true},
		Manifest{MarketID: "crypto", InstrumentType: "spot", DatasetID: "binance_spot_kline_1m", DatasetIDs: []string{"spot_kline_1h"}, DatasetFrequencies: map[string][]string{"binance_spot_kline_1m": {"1m"}, "spot_kline_1h": {"1H"}}, Timezone: "UTC", Frequencies: []string{"1m", "1H"}, Sources: []SourceRef{{ProviderID: "binance", SourceID: "spot_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1m", "1H"}, Status: SourceEnabled}}, Enabled: true},
		Manifest{MarketID: "crypto", InstrumentType: "swap", DatasetID: "binance_swap_kline_1m", DatasetIDs: []string{"perpetual_kline_1h"}, DatasetFrequencies: map[string][]string{"binance_swap_kline_1m": {"1m"}, "perpetual_kline_1h": {"1H"}}, Timezone: "UTC", Frequencies: []string{"1m", "1H"}, Sources: []SourceRef{{ProviderID: "binance", SourceID: "swap_http", ProtocolVariant: "http", Transport: "https", Port: 443, Frequencies: []string{"1m", "1H"}, Status: SourceEnabled}}, Enabled: true},
	)
}
