// Package storagesource loads planner inputs from MooX storage metadata.
package storagesource

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/transport"
)

const storagePageSize = 500

type metadataClient interface {
	GetDataset(ctx context.Context, req *storagepb.GetDatasetReq, opts ...client.Option) (*storagepb.GetDatasetRsp, error)
	ListSubjects(ctx context.Context, req *storagepb.ListSubjectsReq, opts ...client.Option) (*storagepb.ListSubjectsRsp, error)
	ListDatasetSubjects(ctx context.Context, req *storagepb.ListDatasetSubjectsReq, opts ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error)
	ListSubjectSymbols(ctx context.Context, req *storagepb.ListSubjectSymbolsReq, opts ...client.Option) (*storagepb.ListSubjectSymbolsRsp, error)
}

type datasetColumnClient interface {
	ListDatasetColumns(context.Context, *storagepb.ListDatasetColumnsReq, ...client.Option) (*storagepb.ListDatasetColumnsRsp, error)
}

// DatasetInfo is the minimal Dataset contract required by Collector rules.
type DatasetInfo struct {
	DataSourceID string
	DataNodeID   string
	DataKind     storagepb.DataKind
	Status       string
	Freqs        []string
	Columns      []string
	ColumnTypes  map[string]storagepb.FieldValueType
	Attributes   map[string]string
	KeepDuration string
	Revision     uint64
}

// GetDataset returns the Dataset identity and write shape used for rule validation.
func (s *DatasetSource) GetDataset(ctx context.Context, spaceID, datasetID string) (DatasetInfo, error) {
	rsp, err := s.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{
		SpaceId: strings.TrimSpace(spaceID), DatasetId: strings.TrimSpace(datasetID),
	})
	if err != nil {
		return DatasetInfo{}, fmt.Errorf("get dataset: %w", err)
	}
	if err := ensureStorageOK("get dataset", rsp.GetRetInfo()); err != nil {
		return DatasetInfo{}, err
	}
	dataset := rsp.GetDataset()
	if dataset == nil {
		return DatasetInfo{}, fmt.Errorf("get dataset: empty dataset")
	}
	info := DatasetInfo{
		DataSourceID: strings.TrimSpace(dataset.GetDataSourceId()),
		DataNodeID:   strings.TrimSpace(dataset.GetDataNodeId()),
		DataKind:     dataset.GetDataKind(),
		Status:       strings.ToLower(strings.TrimSpace(dataset.GetStatus())),
		Freqs:        append([]string(nil), dataset.GetFreqs()...),
		Attributes:   cloneAttributes(dataset.GetAttributes()),
		KeepDuration: strings.TrimSpace(dataset.GetKeepDuration()),
		Revision:     dataset.GetRevision(),
		ColumnTypes:  make(map[string]storagepb.FieldValueType),
	}
	// The generated Metadata client supports column discovery, while small
	// in-memory test adapters and older control-plane fakes may not. Keep this
	// optional at the adapter boundary; callers that need a K-line schema can
	// reject an empty result or use their explicit compatibility contract.
	if columns, ok := s.metadata.(datasetColumnClient); ok {
		for page := 1; page <= 100; page++ {
			rsp, err := columns.ListDatasetColumns(ctx, &storagepb.ListDatasetColumnsReq{SpaceId: strings.TrimSpace(spaceID), DatasetId: strings.TrimSpace(datasetID), Page: &storagepb.Page{Page: uint32(page), Size: 500}})
			if err != nil {
				return DatasetInfo{}, fmt.Errorf("list dataset columns: %w", err)
			}
			if err := ensureStorageOK("list dataset columns", rsp.GetRetInfo()); err != nil {
				return DatasetInfo{}, err
			}
			for _, column := range rsp.GetColumns() {
				if column != nil && strings.EqualFold(strings.TrimSpace(column.GetStatus()), "active") && strings.TrimSpace(column.GetColumnName()) != "" {
					name := strings.TrimSpace(column.GetColumnName())
					info.Columns = append(info.Columns, name)
					info.ColumnTypes[name] = column.GetValueType()
				}
			}
			if !rsp.GetPageResult().GetHasMore() || len(rsp.GetColumns()) == 0 {
				break
			}
		}
	}
	return info, nil
}

func cloneAttributes(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

// DatasetSource loads active dataset subjects and source-side symbols.
type DatasetSource struct {
	metadata metadataClient
}

// NewDatasetSource creates a storage metadata backed dataset source.
func NewDatasetSource(metadataTarget string) *DatasetSource {
	return &DatasetSource{
		metadata: storagepb.NewMetadataClientProxy(
			append(gatewayauth.NewTRPCClientOptions(normalizeTRPCTarget(metadataTarget, "11003"), gatewayauth.ServiceGatewayNodeID(), gatewayauth.CredentialsFromEnv()),
				client.WithTransport(transport.DefaultClientTransport))...,
		),
	}
}

// ListSubjects returns active dataset subjects enriched with external symbols.
func (s *DatasetSource) ListSubjects(ctx context.Context, spaceID string, datasetID string, dataSourceID string) ([]domain.DatasetSubject, error) {
	if strings.TrimSpace(datasetID) == "" {
		return nil, fmt.Errorf("dataset_id is required")
	}
	bindings, err := s.listDatasetBindings(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	symbols, err := s.listSubjectSymbols(ctx, spaceID, dataSourceID)
	if err != nil {
		return nil, err
	}
	market := ""
	if dataset, datasetErr := s.GetDataset(ctx, spaceID, datasetID); datasetErr == nil {
		market = strings.TrimSpace(dataset.Attributes["market_type"])
	}
	if len(bindings) == 0 && market != "" {
		filtered, filterErr := s.filterSymbolsByMarket(ctx, spaceID, symbols, market)
		if filterErr != nil {
			return nil, filterErr
		}
		symbols = filtered
	}
	return mergeDatasetSubjects(bindings, symbols)
}

// ListResampleSubjects returns the source Dataset's active subject set
// without requiring an external-symbol catalog owned by the source DataSource.
// Shared/normalized K-line result Datasets commonly use data_source_id=crypto_market
// while their exchange symbols remain registered under binance or another venue.
func (s *DatasetSource) ListResampleSubjects(ctx context.Context, spaceID, datasetID string) ([]domain.DatasetSubject, error) {
	return s.ListResampleSubjectsForRule(ctx, spaceID, datasetID, "", "")
}

// ListResampleSubjectsForRule resolves the active subject set with the rule's
// provider/series tag when a shared result Dataset contains multiple venues.
// This keeps an OKX-only Subject out of a Binance resample task even though
// both venues share the same market-level Dataset metadata.
func (s *DatasetSource) ListResampleSubjectsForRule(ctx context.Context, spaceID, datasetID, provider, seriesTag string) ([]domain.DatasetSubject, error) {
	if strings.TrimSpace(datasetID) == "" {
		return nil, fmt.Errorf("dataset_id is required")
	}
	bindings, err := s.listDatasetBindings(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	if len(bindings) > 0 {
		dataset, datasetErr := s.GetDataset(ctx, spaceID, datasetID)
		if datasetErr != nil {
			return nil, datasetErr
		}
		if dataSourceID := strings.TrimSpace(dataset.DataSourceID); dataSourceID != "" && !strings.EqualFold(dataSourceID, "crypto_market") {
			// A provider-owned Dataset may have an explicit subject binding and
			// still require a provider-specific symbol (BTC-USDT -> BTCUSDT).
			symbols, symbolErr := s.listSubjectSymbols(ctx, spaceID, dataSourceID)
			if symbolErr != nil {
				return nil, symbolErr
			}
			return mergeDatasetSubjects(bindings, symbols)
		}
		if strings.EqualFold(strings.TrimSpace(dataset.DataSourceID), "crypto_market") {
			if symbolSource := inferResampleSymbolSource(provider, seriesTag); symbolSource != "" {
				symbols, symbolErr := s.listSubjectSymbols(ctx, spaceID, symbolSource)
				if symbolErr != nil {
					return nil, symbolErr
				}
				if market := strings.TrimSpace(dataset.Attributes["market_type"]); market != "" {
					symbols, symbolErr = s.filterSymbolsByMarket(ctx, spaceID, symbols, market)
					if symbolErr != nil {
						return nil, symbolErr
					}
				}
				if len(symbols) == 0 {
					return nil, fmt.Errorf("no active subject symbols for resample source %q", symbolSource)
				}
				return mergeDatasetSubjectsBySymbol(bindings, symbols)
			}
		}
		return subjectsFromBindings(bindings), nil
	}
	dataset, err := s.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	// Provider-owned source Datasets still use the provider's external-symbol
	// catalog. Falling back to every Subject in the market would create tasks
	// for symbols that the source DataSource cannot supply (for example, an
	// OKX-only symbol on a Binance Dataset).
	if dataSourceID := strings.TrimSpace(dataset.DataSourceID); dataSourceID != "" && !strings.EqualFold(dataSourceID, "crypto_market") {
		symbols, symbolErr := s.listSubjectSymbols(ctx, spaceID, dataSourceID)
		if symbolErr != nil {
			return nil, symbolErr
		}
		if market := strings.TrimSpace(dataset.Attributes["market_type"]); market != "" {
			symbols, symbolErr = s.filterSymbolsByMarket(ctx, spaceID, symbols, market)
			if symbolErr != nil {
				return nil, symbolErr
			}
		}
		return mergeDatasetSubjects(nil, symbols)
	}
	dataSourceID := strings.TrimSpace(dataset.DataSourceID)
	if strings.EqualFold(dataSourceID, "crypto_market") {
		if symbolSource := inferResampleSymbolSource(provider, seriesTag); symbolSource != "" {
			symbols, symbolErr := s.listSubjectSymbols(ctx, spaceID, symbolSource)
			if symbolErr != nil {
				return nil, symbolErr
			}
			if market := strings.TrimSpace(dataset.Attributes["market_type"]); market != "" {
				symbols, symbolErr = s.filterSymbolsByMarket(ctx, spaceID, symbols, market)
				if symbolErr != nil {
					return nil, symbolErr
				}
			}
			if len(symbols) == 0 {
				return nil, fmt.Errorf("no active subject symbols for resample source %q", symbolSource)
			}
			return mergeDatasetSubjects(nil, symbols)
		}
	}
	market := strings.TrimSpace(dataset.Attributes["market_type"])
	if market == "" {
		return nil, fmt.Errorf("source Dataset %s/%s has no market_type", spaceID, datasetID)
	}
	var subjects []domain.DatasetSubject
	for page := uint32(1); ; page++ {
		rsp, callErr := s.metadata.ListSubjects(ctx, &storagepb.ListSubjectsReq{SpaceId: spaceID, Market: market, Page: &storagepb.Page{Page: page, Size: storagePageSize}})
		if callErr != nil {
			return nil, fmt.Errorf("list resample subjects: %w", callErr)
		}
		if err := ensureStorageOK("list resample subjects", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, subject := range rsp.GetSubjects() {
			if subject == nil || !isActive(subject.GetStatus()) || strings.TrimSpace(subject.GetSubjectId()) == "" {
				continue
			}
			subjects = append(subjects, domain.DatasetSubject{SubjectID: subject.GetSubjectId(), SubjectName: subject.GetName(), ExternalSymbol: subject.GetSubjectId(), Status: "active"})
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	return subjects, nil
}

func inferResampleSymbolSource(provider, seriesTag string) string {
	provider = strings.TrimSpace(provider)
	if provider != "" && !strings.EqualFold(provider, "moox") {
		return provider
	}
	parts := strings.SplitN(strings.TrimSpace(seriesTag), ":", 2)
	if len(parts) == 2 && strings.EqualFold(strings.TrimSpace(parts[0]), "venue") {
		return strings.TrimSpace(parts[1])
	}
	return ""
}

func subjectsFromBindings(bindings []*storagepb.DatasetSubject) []domain.DatasetSubject {
	subjects := make([]domain.DatasetSubject, 0, len(bindings))
	for _, binding := range bindings {
		if binding == nil || !isActive(binding.GetStatus()) || strings.TrimSpace(binding.GetSubjectId()) == "" {
			continue
		}
		external := strings.TrimSpace(binding.GetAttributes()["external_symbol"])
		if external == "" {
			external = binding.GetSubjectId()
		}
		subjects = append(subjects, domain.DatasetSubject{SubjectID: binding.GetSubjectId(), SubjectName: binding.GetSubjectId(), ExternalSymbol: external, Status: "active"})
	}
	return subjects
}

func mergeDatasetSubjectsBySymbol(bindings []*storagepb.DatasetSubject, symbols map[string]string) ([]domain.DatasetSubject, error) {
	selected := make([]*storagepb.DatasetSubject, 0, len(bindings))
	for _, binding := range bindings {
		if binding == nil || !isActive(binding.GetStatus()) {
			continue
		}
		if _, ok := symbols[binding.GetSubjectId()]; ok {
			selected = append(selected, binding)
		}
	}
	// An explicit DatasetSubject set is an allow-list. Do not let an empty
	// intersection fall through to mergeDatasetSubjects' empty-binding
	// active-subject fallback, which would silently select every provider symbol.
	if len(bindings) > 0 && len(selected) == 0 {
		return []domain.DatasetSubject{}, nil
	}
	result, err := mergeDatasetSubjects(selected, symbols)
	if err != nil {
		return nil, err
	}
	return result, nil
}

func (s *DatasetSource) filterSymbolsByMarket(ctx context.Context, spaceID string, symbols map[string]string, market string) (map[string]string, error) {
	allowed := make(map[string]struct{})
	for page := uint32(1); ; page++ {
		rsp, err := s.metadata.ListSubjects(ctx, &storagepb.ListSubjectsReq{SpaceId: spaceID, Market: market, Page: &storagepb.Page{Page: page, Size: storagePageSize}})
		if err != nil {
			return nil, fmt.Errorf("list subjects by market: %w", err)
		}
		if err := ensureStorageOK("list subjects by market", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, subject := range rsp.GetSubjects() {
			if subject != nil && isActive(subject.GetStatus()) {
				allowed[subject.GetSubjectId()] = struct{}{}
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	filtered := make(map[string]string, len(symbols))
	for subjectID, external := range symbols {
		if _, ok := allowed[subjectID]; ok {
			filtered[subjectID] = external
		}
	}
	return filtered, nil
}

func (s *DatasetSource) listDatasetBindings(ctx context.Context, spaceID string, datasetID string) ([]*storagepb.DatasetSubject, error) {
	var all []*storagepb.DatasetSubject
	for page := uint32(1); ; page++ {
		rsp, err := s.metadata.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{
			SpaceId:   spaceID,
			DatasetId: datasetID,
			Page:      &storagepb.Page{Page: page, Size: storagePageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("list dataset subjects: %w", err)
		}
		if err := ensureStorageOK("list dataset subjects", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		all = append(all, rsp.GetDatasetSubjects()...)
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	return all, nil
}

func (s *DatasetSource) listSubjectSymbols(ctx context.Context, spaceID string, dataSourceID string) (map[string]string, error) {
	symbols := make(map[string]string)
	for page := uint32(1); ; page++ {
		rsp, err := s.metadata.ListSubjectSymbols(ctx, &storagepb.ListSubjectSymbolsReq{
			SpaceId:      spaceID,
			DataSourceId: dataSourceID,
			Page:         &storagepb.Page{Page: page, Size: storagePageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("list subject symbols: %w", err)
		}
		if err := ensureStorageOK("list subject symbols", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, item := range rsp.GetSubjectSymbols() {
			if item.GetSubjectId() == "" || !isActive(item.GetStatus()) {
				continue
			}
			if item.GetExternalSymbol() != "" {
				if _, exists := symbols[item.GetSubjectId()]; exists {
					return nil, fmt.Errorf("subject %s has duplicate active external symbols", item.GetSubjectId())
				}
				symbols[item.GetSubjectId()] = item.GetExternalSymbol()
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	return symbols, nil
}

func mergeDatasetSubjects(
	bindings []*storagepb.DatasetSubject,
	symbols map[string]string,
) ([]domain.DatasetSubject, error) {
	// Runtime K-line Datasets are often written for the full exchange symbol
	// active subject set without maintaining a duplicate DatasetSubject row for every
	// symbol. In that case the active data-source symbol catalog is the explicit
	// active subject set and keeps local resample planning useful for existing result data.
	if len(bindings) == 0 && len(symbols) > 0 {
		ids := make([]string, 0, len(symbols))
		for subjectID := range symbols {
			ids = append(ids, subjectID)
		}
		sort.Strings(ids)
		result := make([]domain.DatasetSubject, 0, len(ids))
		for _, subjectID := range ids {
			result = append(result, domain.DatasetSubject{SubjectID: subjectID, SubjectName: subjectID, ExternalSymbol: symbols[subjectID], Status: "active"})
		}
		return result, nil
	}
	subjects := make([]domain.DatasetSubject, 0, len(bindings))
	for _, binding := range bindings {
		if binding.GetSubjectId() == "" || !isActive(binding.GetStatus()) {
			continue
		}
		external := strings.TrimSpace(symbols[binding.GetSubjectId()])
		if external == "" {
			return nil, fmt.Errorf("active dataset subject %s has no active external symbol", binding.GetSubjectId())
		}
		subjects = append(subjects, domain.DatasetSubject{
			SubjectID:      binding.GetSubjectId(),
			SubjectName:    binding.GetSubjectId(),
			ExternalSymbol: external,
			Status:         binding.GetStatus(),
		})
	}
	return subjects, nil
}

func ensureStorageOK(action string, ret *storagepb.RetInfo) error {
	if ret == nil {
		return fmt.Errorf("%s: empty ret_info", action)
	}
	if ret.GetCode() != storagepb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s", action, ret.GetMsg())
	}
	return nil
}

func isActive(status string) bool {
	return strings.EqualFold(strings.TrimSpace(status), "active")
}

func normalizeTRPCTarget(raw string, defaultPort string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "ip://127.0.0.1:" + defaultPort
	}
	if strings.HasPrefix(raw, "ip://") {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return raw
	}
	if err == nil && parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return raw
	}
	if err == nil && parsed.Host != "" {
		return "ip://" + parsed.Host
	}
	if strings.Contains(raw, "://") || !strings.Contains(raw, ":") {
		return raw
	}
	return "ip://" + raw
}
