package marketfetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
)

const (
	StockCNInstrumentDatasetID = "dataset_stockcn_instruments"
	StockCNDataSourceID        = "stockcn"
	// Keep each PrimaryStore request bounded while avoiding hundreds of
	// sequential RPCs for the full stock catalogue.
	instrumentStorageRowsPerBatch = 500
)

type InstrumentStorage interface {
	Storage
	ListDatasetSubjects(context.Context, string, string) ([]*storagepb.DatasetSubject, error)
	ListSubjectSymbols(context.Context, string, string) ([]*storagepb.SubjectSymbol, error)
	StageDatasetSubjectSet(context.Context, string, string, []*storagepb.DatasetSubject) error
	ActivateDatasetSubjectSet(context.Context, string, string) error
}

type InstrumentPipeline struct {
	Registry       *marketdata.Registry
	Storage        InstrumentStorage
	CandidateChain []string
	// InstrumentProviderTimeout bounds the time spent waiting for one complete
	// provider snapshot. A slow source must not consume the whole SCF budget
	// after another source has already produced a usable snapshot.
	InstrumentProviderTimeout time.Duration
	SpaceID                   string
	MarketID                  string
	DatasetID                 string
	TargetDatasetID           string
	DataSourceID              string
	SubjectType               string
	SubjectMarket             string
	Currency                  string
	Timezone                  string
	InstrumentType            string
	RequiredExchanges         []string
	MinimumCount              int
	RouteID                   string
	Metrics                   *Metrics
	Now                       func() time.Time
}

type InstrumentPipelineRequest struct {
	RequestID          string    `json:"request_id"`
	SnapshotAt         time.Time `json:"snapshot_at"`
	SnapshotShardIndex int       `json:"snapshot_shard_index,omitempty"`
	SnapshotShardCount int       `json:"snapshot_shard_count,omitempty"`
}

type datasetSubjectSetActivator interface {
	ActivateDatasetSubjectSetWithCount(context.Context, string, string) (int, error)
}

type InstrumentPipelineResult struct {
	SnapshotID       string         `json:"snapshot_id"`
	SourceProvider   string         `json:"source_provider"`
	FetchedAt        time.Time      `json:"fetched_at"`
	Complete         bool           `json:"complete"`
	PageCount        int            `json:"page_count"`
	InstrumentCount  int            `json:"instrument_count"`
	ExchangeCounts   map[string]int `json:"exchange_counts"`
	ActiveSetVersion string         `json:"active_instrument_set_version"`
}

type instrumentFetchResult struct {
	providerID string
	snapshot   marketdata.InstrumentSnapshot
	err        error
	done       bool
}

func (p *InstrumentPipeline) Execute(ctx context.Context, req InstrumentPipelineRequest) (InstrumentPipelineResult, error) {
	if p == nil || p.Registry == nil || p.Storage == nil {
		return InstrumentPipelineResult{}, fmt.Errorf("instrument pipeline is not initialized")
	}
	if strings.TrimSpace(req.RequestID) == "" {
		return InstrumentPipelineResult{}, fmt.Errorf("instrument request_id is required")
	}
	if req.SnapshotAt.IsZero() {
		req.SnapshotAt = time.Now().UTC()
		if p.Now != nil {
			req.SnapshotAt = p.Now().UTC()
		}
	}
	marketID := firstNonEmptyString(p.MarketID, StockCNSpaceID)
	chain := uniqueProviders(p.CandidateChain)
	if len(chain) == 0 {
		return InstrumentPipelineResult{}, fmt.Errorf("instrument candidate chain is empty")
	}

	var snapshot marketdata.InstrumentSnapshot
	var lastErr error
	selectedProvider := "none"
	metricResult := "invalid"
	defer func() {
		if p.Metrics == nil {
			return
		}
		active := 0
		exchanges := map[string]int(nil)
		if lastErr == nil {
			metricResult = "success"
			active = len(snapshot.Instruments)
			exchanges = snapshot.ExchangeCounts
		}
		routeID := firstNonEmptyString(p.RouteID, instrumentRouteID(marketID, p.InstrumentType))
		p.Metrics.ObserveInstrumentSnapshot(marketID, routeID, selectedProvider, metricResult, active, exchanges, snapshot.FetchedAt)
	}()
	// Fetch every configured complete snapshot concurrently. A provider failure
	// is isolated so a healthy source can still supply the active set; only the
	// merged result is allowed to pass the market-wide completeness checks.
	providerTimeout := p.InstrumentProviderTimeout
	if providerTimeout <= 0 {
		providerTimeout = 30 * time.Second
	}
	fetches := fetchInstrumentSnapshots(ctx, p.Registry, chain, marketdata.InstrumentRequest{MarketID: marketdata.MarketID(marketID), SnapshotAt: req.SnapshotAt.UTC(), RequestID: req.RequestID}, providerTimeout)
	validSnapshots := make([]marketdata.InstrumentSnapshot, 0, len(fetches))
	validProviders := make([]string, 0, len(fetches))
	fetchErrors := make([]error, 0, len(fetches))
	for _, fetch := range fetches {
		if fetch.err != nil {
			fetchErrors = append(fetchErrors, fmt.Errorf("instrument provider %s: %w", fetch.providerID, fetch.err))
			continue
		}
		if err := p.validateInstrumentSourceSnapshot(fetch.snapshot, marketID, fetch.providerID, req.SnapshotAt.UTC()); err != nil {
			fetchErrors = append(fetchErrors, fmt.Errorf("instrument provider %s: %w", fetch.providerID, err))
			continue
		}
		validSnapshots = append(validSnapshots, fetch.snapshot)
		validProviders = append(validProviders, fetch.providerID)
	}
	if len(validSnapshots) == 0 {
		lastErr = fmt.Errorf("all instrument providers failed: %w", errors.Join(fetchErrors...))
		metricResult = instrumentMetricResult(lastErr)
		return InstrumentPipelineResult{}, lastErr
	}
	selectedProvider = strings.Join(validProviders, "+")
	var mergeErr error
	snapshot, mergeErr = mergeInstrumentSnapshots(validSnapshots, marketID)
	if mergeErr != nil {
		lastErr = fmt.Errorf("merge instrument snapshots: %w", mergeErr)
		metricResult = instrumentMetricResult(lastErr)
		return InstrumentPipelineResult{}, lastErr
	}
	if err := p.validateSnapshot(snapshot, marketID); err != nil {
		lastErr = fmt.Errorf("merged instrument snapshot: %w", err)
		metricResult = instrumentMetricResult(lastErr)
		return InstrumentPipelineResult{}, lastErr
	}
	// A sharded invocation must use one generation fence for every shard. The
	// content fingerprint is stored alongside that fence and Storage rejects a
	// mixed-provider generation before activation.
	contentFingerprint := snapshotContentFingerprint(snapshot)
	if req.SnapshotShardCount > 0 {
		// Keep the generation identity independent of the fetched contents. Every
		// shard must stage under one ID; Storage compares the content fingerprint
		// and rejects a mixed-provider generation atomically.
		snapshot.SnapshotID = instrumentSnapshotGenerationID(marketID, chain, req.SnapshotAt, p.DatasetID, p.TargetDatasetID, p.InstrumentType)
	} else {
		snapshot.SnapshotID = snapshotGenerationID(snapshot)
	}
	lastErr = nil
	if req.SnapshotShardCount > 0 {
		shardSnapshot, shardIndex, shardCount, shardErr := instrumentSnapshotShard(snapshot, req.SnapshotShardIndex, req.SnapshotShardCount)
		if shardErr != nil {
			lastErr = shardErr
			metricResult = "incomplete"
			return InstrumentPipelineResult{}, shardErr
		}
		if shardIndex < shardCount {
			activated, persistErr := p.persistSnapshotShard(ctx, snapshot, shardSnapshot, shardIndex, shardCount, contentFingerprint)
			if persistErr != nil {
				lastErr = persistErr
				metricResult = "invalid"
				return InstrumentPipelineResult{}, persistErr
			}
			activeSetVersion := ""
			if activated {
				activeSetVersion = snapshot.SnapshotID
			}
			return InstrumentPipelineResult{SnapshotID: snapshot.SnapshotID, SourceProvider: snapshot.SourceProvider, FetchedAt: snapshot.FetchedAt, Complete: snapshot.Complete, PageCount: snapshot.PageCount, InstrumentCount: len(shardSnapshot.Instruments), ExchangeCounts: cloneCounts(snapshot.ExchangeCounts), ActiveSetVersion: activeSetVersion}, nil
		}
		// The scheduler uses a fixed upper bound of snapshot shards. When a
		// small crypto catalogue needs fewer shards, the unused invocations are
		// harmless no-ops rather than empty staging requests.
		return InstrumentPipelineResult{SnapshotID: snapshot.SnapshotID, SourceProvider: snapshot.SourceProvider, FetchedAt: snapshot.FetchedAt, Complete: snapshot.Complete, PageCount: snapshot.PageCount, InstrumentCount: 0, ExchangeCounts: cloneCounts(snapshot.ExchangeCounts)}, nil
	}
	if err := p.persistSnapshot(ctx, snapshot); err != nil {
		lastErr = err
		metricResult = "invalid"
		return InstrumentPipelineResult{}, err
	}
	return InstrumentPipelineResult{SnapshotID: snapshot.SnapshotID, SourceProvider: snapshot.SourceProvider, FetchedAt: snapshot.FetchedAt, Complete: snapshot.Complete, PageCount: snapshot.PageCount, InstrumentCount: len(snapshot.Instruments), ExchangeCounts: cloneCounts(snapshot.ExchangeCounts), ActiveSetVersion: snapshot.SnapshotID}, nil
}

func fetchInstrumentSnapshots(ctx context.Context, registry *marketdata.Registry, chain []string, req marketdata.InstrumentRequest, providerTimeout time.Duration) []instrumentFetchResult {
	results := make([]instrumentFetchResult, len(chain))
	resultCh := make(chan instrumentFetchResult, len(chain))
	active := 0
	for index, providerID := range chain {
		results[index] = instrumentFetchResult{providerID: providerID}
		fetcher, err := registry.InstrumentFetcher(providerID)
		if err != nil {
			results[index].err = err
			results[index].done = true
			continue
		}
		status := fetcher.Descriptor().Status
		if status == marketdata.SourceShadow || status == marketdata.SourceCatalogOnly {
			results[index].err = fmt.Errorf("%w: %s", marketdata.ErrSourceUnavailable, fetcher.Descriptor().SourceKey().String())
			results[index].done = true
			continue
		}
		active++
		go func(providerID string, fetcher marketdata.InstrumentFetcher) {
			providerCtx, cancel := context.WithTimeout(ctx, providerTimeout)
			defer cancel()
			snapshot, err := fetcher.FetchInstrumentSnapshot(providerCtx, req)
			resultCh <- instrumentFetchResult{providerID: providerID, snapshot: snapshot, err: err}
		}(providerID, fetcher)
	}
	if active == 0 {
		return results
	}
	completed := 0
	timer := time.NewTimer(providerTimeout)
	defer timer.Stop()
	for completed < active {
		select {
		case result := <-resultCh:
			for index := range results {
				if results[index].providerID == result.providerID && !results[index].done {
					results[index] = result
					results[index].done = true
					break
				}
			}
			completed++
		case <-ctx.Done():
			for index := range results {
				if !results[index].done {
					results[index].err = ctx.Err()
					results[index].done = true
				}
			}
			return results
		case <-timer.C:
			for index := range results {
				if !results[index].done {
					results[index].err = fmt.Errorf("%w: provider snapshot deadline exceeded", marketdata.ErrTimeout)
					results[index].done = true
				}
			}
			return results
		}
	}
	return results
}

func (p *InstrumentPipeline) validateInstrumentSourceSnapshot(snapshot marketdata.InstrumentSnapshot, marketID, providerID string, expectedFetchedAt time.Time) error {
	if err := marketdata.ValidateInstrumentSnapshot(snapshot); err != nil {
		return fmt.Errorf("invalid complete snapshot: %w", err)
	}
	if snapshot.MarketID != marketID {
		return fmt.Errorf("snapshot market %q does not match %q", snapshot.MarketID, marketID)
	}
	if !strings.EqualFold(strings.TrimSpace(snapshot.SourceProvider), strings.TrimSpace(providerID)) {
		return fmt.Errorf("snapshot source_provider %q does not match registered provider %q", snapshot.SourceProvider, providerID)
	}
	if !snapshot.FetchedAt.UTC().Equal(expectedFetchedAt.UTC()) {
		return fmt.Errorf("snapshot fetched_at %s does not match request snapshot_at %s", snapshot.FetchedAt.UTC().Format(time.RFC3339Nano), expectedFetchedAt.UTC().Format(time.RFC3339Nano))
	}
	if p.MinimumCount > 0 && len(snapshot.Instruments) < p.MinimumCount {
		return fmt.Errorf("source snapshot count %d is below minimum %d", len(snapshot.Instruments), p.MinimumCount)
	}
	for _, exchange := range p.RequiredExchanges {
		if snapshot.ExchangeCounts[exchange] <= 0 {
			return fmt.Errorf("source snapshot is missing exchange %s", exchange)
		}
	}
	return nil
}

func mergeInstrumentSnapshots(snapshots []marketdata.InstrumentSnapshot, marketID string) (marketdata.InstrumentSnapshot, error) {
	if len(snapshots) == 0 {
		return marketdata.InstrumentSnapshot{}, nil
	}
	providers := make([]string, 0, len(snapshots))
	fetchedAt := snapshots[0].FetchedAt.UTC()
	pageCount := 0
	bySubject := make(map[string]marketdata.Instrument)
	for _, snapshot := range snapshots {
		providers = append(providers, snapshot.SourceProvider)
		if snapshot.FetchedAt.After(fetchedAt) {
			fetchedAt = snapshot.FetchedAt.UTC()
		}
		pageCount += snapshot.PageCount
		for _, instrument := range snapshot.Instruments {
			current, exists := bySubject[instrument.SubjectID]
			if !exists {
				bySubject[instrument.SubjectID] = instrument
				continue
			}
			merged, err := mergeInstrumentMetadata(current, instrument)
			if err != nil {
				return marketdata.InstrumentSnapshot{}, fmt.Errorf("subject %s: %w", instrument.SubjectID, err)
			}
			bySubject[instrument.SubjectID] = merged
		}
	}
	instruments := make([]marketdata.Instrument, 0, len(bySubject))
	for _, instrument := range bySubject {
		instruments = append(instruments, instrument)
	}
	sort.Slice(instruments, func(i, j int) bool { return instruments[i].SubjectID < instruments[j].SubjectID })
	exchangeCounts := make(map[string]int)
	for _, instrument := range instruments {
		exchangeCounts[instrument.Exchange]++
	}
	sourceProvider := strings.Join(providers, "+")
	return marketdata.InstrumentSnapshot{
		SnapshotID:     marketdata.SnapshotID(sourceProvider, marketID, fetchedAt),
		SourceProvider: sourceProvider,
		MarketID:       marketID,
		FetchedAt:      fetchedAt,
		Complete:       true,
		PageCount:      pageCount,
		ExchangeCounts: exchangeCounts,
		Instruments:    instruments,
	}, nil
}

func mergeInstrumentMetadata(primary, secondary marketdata.Instrument) (marketdata.Instrument, error) {
	// ProviderSymbol is deliberately excluded from conflict detection: it is a
	// provider-facing alias, while SubjectID is the cross-provider identity.
	// For descriptive fields, the first configured provider wins and blanks are
	// filled from the later provider; canonical identity and lifecycle status
	// remain strict conflicts above.
	for _, field := range []struct {
		name      string
		primary   string
		secondary string
	}{
		{name: "canonical_symbol", primary: primary.CanonicalSymbol, secondary: secondary.CanonicalSymbol},
		{name: "exchange", primary: primary.Exchange, secondary: secondary.Exchange},
		{name: "status", primary: primary.Status, secondary: secondary.Status},
	} {
		if strings.TrimSpace(field.primary) != "" && strings.TrimSpace(field.secondary) != "" && field.primary != field.secondary {
			return marketdata.Instrument{}, fmt.Errorf("conflicting %s values %q and %q", field.name, field.primary, field.secondary)
		}
	}
	if primary.CanonicalSymbol == "" {
		primary.CanonicalSymbol = secondary.CanonicalSymbol
	}
	if primary.Name == "" {
		primary.Name = secondary.Name
	}
	if primary.Status == "" {
		primary.Status = secondary.Status
	}
	if primary.BaseAsset == "" {
		primary.BaseAsset = secondary.BaseAsset
	}
	if primary.QuoteAsset == "" {
		primary.QuoteAsset = secondary.QuoteAsset
	}
	if primary.MinQty == "" {
		primary.MinQty = secondary.MinQty
	}
	if primary.MaxQty == "" {
		primary.MaxQty = secondary.MaxQty
	}
	if primary.TickSize == "" {
		primary.TickSize = secondary.TickSize
	}
	if primary.LotSize == "" {
		primary.LotSize = secondary.LotSize
	}
	return primary, nil
}

func instrumentSnapshotGenerationID(marketID string, chain []string, snapshotAt time.Time, scopeValues ...string) string {
	var builder strings.Builder
	builder.WriteString(strings.TrimSpace(marketID))
	builder.WriteByte(0)
	builder.WriteString(snapshotAt.UTC().Format(time.RFC3339Nano))
	builder.WriteByte(0)
	for _, scopeValue := range scopeValues {
		builder.WriteString(strings.TrimSpace(scopeValue))
		builder.WriteByte(0)
	}
	for _, providerID := range chain {
		builder.WriteString(strings.ToLower(strings.TrimSpace(providerID)))
		builder.WriteByte(0)
	}
	hash := sha256.Sum256([]byte(builder.String()))
	return "instrument:" + strings.TrimSpace(marketID) + ":" + snapshotAt.UTC().Format("20060102T150405Z") + ":" + hex.EncodeToString(hash[:8])
}

func snapshotContentFingerprint(snapshot marketdata.InstrumentSnapshot) string {
	ordered := append([]marketdata.Instrument(nil), snapshot.Instruments...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].SubjectID < ordered[j].SubjectID })
	var builder strings.Builder
	for _, value := range []string{snapshot.SourceProvider, snapshot.MarketID, snapshot.FetchedAt.UTC().Format(time.RFC3339Nano), strconv.Itoa(snapshot.PageCount)} {
		builder.WriteString(value)
		builder.WriteByte(0)
	}
	for _, instrument := range ordered {
		for _, value := range []string{instrument.SubjectID, instrument.CanonicalSymbol, instrument.ProviderSymbol, instrument.Exchange, instrument.Name, instrument.Status, instrument.BaseAsset, instrument.QuoteAsset, instrument.MinQty, instrument.MaxQty, instrument.TickSize, instrument.LotSize} {
			builder.WriteString(value)
			builder.WriteByte(0)
		}
	}
	exchanges := make([]string, 0, len(snapshot.ExchangeCounts))
	for exchange := range snapshot.ExchangeCounts {
		exchanges = append(exchanges, exchange)
	}
	sort.Strings(exchanges)
	for _, exchange := range exchanges {
		builder.WriteString(exchange)
		builder.WriteByte(0)
		builder.WriteString(strconv.Itoa(snapshot.ExchangeCounts[exchange]))
		builder.WriteByte(0)
	}
	hash := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(hash[:])
}

func snapshotGenerationID(snapshot marketdata.InstrumentSnapshot) string {
	ordered := append([]marketdata.Instrument(nil), snapshot.Instruments...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].SubjectID < ordered[j].SubjectID })
	var builder strings.Builder
	builder.WriteString(snapshot.SnapshotID)
	builder.WriteByte(0)
	for _, instrument := range ordered {
		for _, value := range []string{instrument.SubjectID, instrument.CanonicalSymbol, instrument.ProviderSymbol, instrument.Exchange, instrument.Name, instrument.Status, instrument.BaseAsset, instrument.QuoteAsset, instrument.MinQty, instrument.MaxQty, instrument.TickSize, instrument.LotSize} {
			builder.WriteString(value)
			builder.WriteByte(0)
		}
	}
	exchanges := make([]string, 0, len(snapshot.ExchangeCounts))
	for exchange := range snapshot.ExchangeCounts {
		exchanges = append(exchanges, exchange)
	}
	sort.Strings(exchanges)
	for _, exchange := range exchanges {
		builder.WriteString(exchange)
		builder.WriteByte(0)
		builder.WriteString(strconv.Itoa(snapshot.ExchangeCounts[exchange]))
		builder.WriteByte(0)
	}
	hash := sha256.Sum256([]byte(builder.String()))
	return snapshot.SnapshotID + ":" + hex.EncodeToString(hash[:8])
}

func instrumentSnapshotShard(snapshot marketdata.InstrumentSnapshot, index, count int) (marketdata.InstrumentSnapshot, int, int, error) {
	if count <= 0 {
		return snapshot, 0, 0, fmt.Errorf("snapshot shard count must be positive")
	}
	if index < 0 || index >= count {
		return snapshot, 0, 0, fmt.Errorf("snapshot shard %d is outside [0,%d)", index, count)
	}
	ordered := append([]marketdata.Instrument(nil), snapshot.Instruments...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].SubjectID < ordered[j].SubjectID })
	effectiveCount := count
	if len(ordered) < effectiveCount {
		effectiveCount = len(ordered)
	}
	if effectiveCount == 0 {
		return snapshot, index, 0, fmt.Errorf("complete instrument snapshot is empty")
	}
	if index >= effectiveCount {
		return snapshot, index, effectiveCount, nil
	}
	start := len(ordered) * index / effectiveCount
	end := len(ordered) * (index + 1) / effectiveCount
	shard := snapshot
	shard.Instruments = ordered[start:end]
	return shard, index, effectiveCount, nil
}

func instrumentMetricResult(err error) string {
	if err == nil {
		return "success"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "stale") || strings.Contains(message, "older than active") {
		return "stale"
	}
	if strings.Contains(message, "incomplete") || (strings.Contains(message, "snapshot") && strings.Contains(message, "page")) {
		return "incomplete"
	}
	return "invalid"
}

func instrumentRouteID(marketID, instrumentType string) string {
	if strings.EqualFold(strings.TrimSpace(marketID), StockCNSpaceID) {
		return "stockcn_instrument_v1"
	}
	if strings.EqualFold(strings.TrimSpace(instrumentType), "swap") {
		return "binance_swap_instrument_v1"
	}
	return "binance_spot_instrument_v1"
}

func (p *InstrumentPipeline) validateSnapshot(snapshot marketdata.InstrumentSnapshot, marketID string) error {
	if err := marketdata.ValidateInstrumentSnapshot(snapshot); err != nil {
		return fmt.Errorf("invalid instrument snapshot: %w", err)
	}
	if snapshot.MarketID != marketID {
		return fmt.Errorf("instrument snapshot market %q does not match %q", snapshot.MarketID, marketID)
	}
	if p.MinimumCount > 0 && len(snapshot.Instruments) < p.MinimumCount {
		return fmt.Errorf("instrument snapshot count %d is below minimum %d", len(snapshot.Instruments), p.MinimumCount)
	}
	for _, exchange := range p.RequiredExchanges {
		if snapshot.ExchangeCounts[exchange] <= 0 {
			return fmt.Errorf("instrument snapshot is missing exchange %s", exchange)
		}
	}
	return nil
}

func (p *InstrumentPipeline) persistSnapshot(ctx context.Context, snapshot marketdata.InstrumentSnapshot) error {
	spaceID := firstNonEmptyString(p.SpaceID, p.MarketID, StockCNSpaceID)
	datasetID := firstNonEmptyString(p.DatasetID, StockCNInstrumentDatasetID)
	targetDatasetID := strings.TrimSpace(p.TargetDatasetID)
	if targetDatasetID == "" && strings.EqualFold(spaceID, StockCNSpaceID) {
		targetDatasetID = StockCNDatasetID
	}
	dataSourceID := firstNonEmptyString(p.DataSourceID, StockCNDataSourceID)
	existing, err := p.Storage.ListDatasetSubjects(ctx, spaceID, datasetID)
	if err != nil {
		return fmt.Errorf("list active instrument set: %w", err)
	}
	var targetExisting []*storagepb.DatasetSubject
	if targetDatasetID != "" && targetDatasetID != datasetID {
		targetExisting, err = p.Storage.ListDatasetSubjects(ctx, spaceID, targetDatasetID)
		if err != nil {
			return fmt.Errorf("list target instrument set: %w", err)
		}
	}
	if publishedAt := newestInstrumentSnapshotAt(existing, targetExisting); !publishedAt.IsZero() && snapshot.FetchedAt.UTC().Before(publishedAt) {
		return fmt.Errorf("instrument snapshot %s is older than active snapshot fetched at %s", snapshot.SnapshotID, publishedAt.UTC().Format(time.RFC3339Nano))
	}
	knownSubjects := instrumentSubjectIDs(existing, targetExisting)
	if strings.EqualFold(spaceID, "crypto") {
		symbols, symbolErr := p.Storage.ListSubjectSymbols(ctx, spaceID, dataSourceID)
		if symbolErr != nil {
			return fmt.Errorf("list existing crypto symbol mappings: %w", symbolErr)
		}
		knownSubjects = registeredSubjectIDs(symbols)
	}
	rows := make([]*storagepb.RowFieldUpsert, 0, len(snapshot.Instruments))
	present := make(map[string]marketdata.Instrument, len(snapshot.Instruments))
	sort.Slice(snapshot.Instruments, func(i, j int) bool { return snapshot.Instruments[i].SubjectID < snapshot.Instruments[j].SubjectID })
	for _, instrument := range snapshot.Instruments {
		if strings.EqualFold(strings.TrimSpace(instrument.Status), "active") {
			present[instrument.SubjectID] = instrument
		}
		rows = append(rows, instrumentRecordRow(spaceID, datasetID, snapshot, instrument))
	}
	for start := 0; start < len(rows); start += instrumentStorageRowsPerBatch {
		end := start + instrumentStorageRowsPerBatch
		if end > len(rows) {
			end = len(rows)
		}
		if err := p.Storage.UpsertFields(ctx, rows[start:end]); err != nil {
			return fmt.Errorf("write instrument snapshot rows %d..%d: %w", start, end, err)
		}
	}
	registerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan marketdata.Instrument)
	errCh := make(chan error, 1)
	var workers sync.WaitGroup
	for worker := 0; worker < 5; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for instrument := range jobs {
				if !strings.EqualFold(spaceID, "crypto") {
					if _, known := knownSubjects[instrument.SubjectID]; known {
						continue
					}
				}
				if err := p.Storage.RegisterDataSubject(registerCtx, p.instrumentRegistration(spaceID, dataSourceID, snapshot, instrument)); err != nil {
					select {
					case errCh <- fmt.Errorf("register instrument %s: %w", instrument.SubjectID, err):
						cancel()
					default:
					}
					return
				}
			}
		}()
	}
	for _, instrument := range snapshot.Instruments {
		select {
		case jobs <- instrument:
		case <-registerCtx.Done():
			break
		}
		if registerCtx.Err() != nil {
			break
		}
	}
	close(jobs)
	workers.Wait()
	select {
	case err := <-errCh:
		return err
	default:
	}
	bindings := stagedInstrumentBindings(spaceID, existing, targetExisting, present, datasetID, targetDatasetID, snapshot, snapshotContentFingerprint(snapshot))
	if err := p.Storage.StageDatasetSubjectSet(ctx, spaceID, snapshot.SnapshotID, bindings); err != nil {
		return fmt.Errorf("stage active instrument set %s: %w", snapshot.SnapshotID, err)
	}
	if err := p.Storage.ActivateDatasetSubjectSet(ctx, spaceID, snapshot.SnapshotID); err != nil {
		return fmt.Errorf("activate active instrument set %s: %w", snapshot.SnapshotID, err)
	}
	return nil
}

// persistSnapshotShard keeps the expensive provider snapshot read complete,
// but splits Storage row/subject work across the fixed Invoke shards. Each
// shard stages only its slice under one generation fence; Storage activates the
// union only after every expected shard has arrived. A failed or late shard
// therefore leaves the previous active set untouched.
func (p *InstrumentPipeline) persistSnapshotShard(ctx context.Context, fullSnapshot, snapshot marketdata.InstrumentSnapshot, shardIndex, shardCount int, contentFingerprint string) (bool, error) {
	spaceID := firstNonEmptyString(p.SpaceID, p.MarketID, StockCNSpaceID)
	datasetID := firstNonEmptyString(p.DatasetID, StockCNInstrumentDatasetID)
	targetDatasetID := strings.TrimSpace(p.TargetDatasetID)
	if targetDatasetID == "" && strings.EqualFold(spaceID, StockCNSpaceID) {
		targetDatasetID = StockCNDatasetID
	}
	dataSourceID := firstNonEmptyString(p.DataSourceID, StockCNDataSourceID)
	existing, err := p.Storage.ListDatasetSubjects(ctx, spaceID, datasetID)
	if err != nil {
		return false, fmt.Errorf("list active instrument shard baseline: %w", err)
	}
	var targetExisting []*storagepb.DatasetSubject
	if targetDatasetID != "" && targetDatasetID != datasetID {
		targetExisting, err = p.Storage.ListDatasetSubjects(ctx, spaceID, targetDatasetID)
		if err != nil {
			return false, fmt.Errorf("list target instrument shard baseline: %w", err)
		}
	}
	knownSubjects := instrumentSubjectIDs(existing, targetExisting)
	if strings.EqualFold(spaceID, "crypto") {
		symbols, symbolErr := p.Storage.ListSubjectSymbols(ctx, spaceID, dataSourceID)
		if symbolErr != nil {
			return false, fmt.Errorf("list existing crypto symbol mappings: %w", symbolErr)
		}
		knownSubjects = registeredSubjectIDs(symbols)
	}
	// Provider fetching is intentionally full-snapshot per shard, but metadata
	// writes must use only this shard's slice. Writing the full catalogue from
	// every shard multiplies Storage work by the fixed fan-out and can outlive
	// the SCF Invoke budget.
	rows := make([]*storagepb.RowFieldUpsert, 0, len(snapshot.Instruments))
	present := make(map[string]marketdata.Instrument, len(snapshot.Instruments))
	for _, instrument := range snapshot.Instruments {
		if strings.EqualFold(strings.TrimSpace(instrument.Status), "active") {
			present[instrument.SubjectID] = instrument
		}
		rows = append(rows, instrumentRecordRow(spaceID, datasetID, snapshot, instrument))
	}
	for start := 0; start < len(rows); start += instrumentStorageRowsPerBatch {
		end := start + instrumentStorageRowsPerBatch
		if end > len(rows) {
			end = len(rows)
		}
		if err := p.Storage.UpsertFields(ctx, rows[start:end]); err != nil {
			return false, fmt.Errorf("write instrument snapshot shard rows %d..%d: %w", start, end, err)
		}
	}
	registerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan marketdata.Instrument)
	errCh := make(chan error, 1)
	var workers sync.WaitGroup
	for worker := 0; worker < 5; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for instrument := range jobs {
				if !strings.EqualFold(spaceID, "crypto") {
					if _, known := knownSubjects[instrument.SubjectID]; known {
						continue
					}
				}
				if err := p.Storage.RegisterDataSubject(registerCtx, p.instrumentRegistration(spaceID, dataSourceID, snapshot, instrument)); err != nil {
					select {
					case errCh <- fmt.Errorf("register instrument %s: %w", instrument.SubjectID, err):
						cancel()
					default:
					}
					return
				}
			}
		}()
	}
	for _, instrument := range snapshot.Instruments {
		select {
		case jobs <- instrument:
		case <-registerCtx.Done():
			break
		}
		if registerCtx.Err() != nil {
			break
		}
	}
	close(jobs)
	workers.Wait()
	select {
	case err := <-errCh:
		return false, err
	default:
	}
	bindings := stagedInstrumentBindings(spaceID, nil, nil, present, datasetID, targetDatasetID, fullSnapshot, contentFingerprint)
	if shardIndex == 0 {
		// The active slice is partitioned across shards, but subjects that
		// disappeared from a complete snapshot still need their existing
		// missing-count lifecycle. Put only those legacy bindings in shard zero;
		// including all existing rows would duplicate active rows from peers.
		fullPresent := make(map[string]marketdata.Instrument, len(fullSnapshot.Instruments))
		for _, instrument := range fullSnapshot.Instruments {
			if strings.EqualFold(strings.TrimSpace(instrument.Status), "active") {
				fullPresent[instrument.SubjectID] = instrument
			}
		}
		bindings = append(bindings, stagedInstrumentBindings(spaceID, filterAbsentMemberships(existing, fullPresent), filterAbsentMemberships(targetExisting, fullPresent), nil, datasetID, targetDatasetID, fullSnapshot, contentFingerprint)...)
	}
	stageID := fmt.Sprintf("%s::shard:%d/%d", snapshot.SnapshotID, shardIndex, shardCount)
	if err := p.Storage.StageDatasetSubjectSet(ctx, spaceID, stageID, bindings); err != nil {
		return false, fmt.Errorf("stage instrument snapshot shard %s: %w", stageID, err)
	}
	if activator, ok := p.Storage.(datasetSubjectSetActivator); ok {
		count, err := activator.ActivateDatasetSubjectSetWithCount(ctx, spaceID, snapshot.SnapshotID)
		if err != nil {
			return false, fmt.Errorf("activate instrument snapshot shards %s: %w", snapshot.SnapshotID, err)
		}
		return count > 0, nil
	}
	// Test doubles and older Storage adapters do not expose the response count.
	// Their activation method retains the legacy complete-set contract.
	if err := p.Storage.ActivateDatasetSubjectSet(ctx, spaceID, snapshot.SnapshotID); err != nil {
		return false, fmt.Errorf("activate instrument snapshot shards %s: %w", snapshot.SnapshotID, err)
	}
	return true, nil
}

// stagedInstrumentBindings builds the full desired state for both datasets.
// Every row is sent as one inactive staging set; the storage activation RPC
// swaps both datasets in one transaction, so an interrupted snapshot cannot
// expose a prefix of the new ActiveInstrumentSet.
func stagedInstrumentBindings(spaceID string, existing, targetExisting []*storagepb.DatasetSubject, present map[string]marketdata.Instrument, datasetID, targetDatasetID string, snapshot marketdata.InstrumentSnapshot, contentFingerprint string) []*storagepb.DatasetSubject {
	desired := make(map[string]*storagepb.DatasetSubject, len(existing)+len(targetExisting)+len(present)*2)
	for _, membership := range append(append([]*storagepb.DatasetSubject(nil), existing...), targetExisting...) {
		if membership == nil {
			continue
		}
		key := membership.GetDatasetId() + "\x00" + membership.GetSubjectId()
		desired[key] = proto.Clone(membership).(*storagepb.DatasetSubject)
	}
	setPresent := func(dataset, role, subjectID string) *storagepb.DatasetSubject {
		key := dataset + "\x00" + subjectID
		item := desired[key]
		if item == nil {
			item = &storagepb.DatasetSubject{SpaceId: spaceID, DatasetId: dataset, SubjectId: subjectID, SubjectRole: role}
			desired[key] = item
		}
		item.Status = "active"
		item.Attributes = cloneAttributes(item.GetAttributes())
		item.Attributes["active_instrument_set_version"] = snapshot.SnapshotID
		item.Attributes["active_instrument_set_fetched_at"] = snapshot.FetchedAt.UTC().Format(time.RFC3339Nano)
		item.Attributes["instrument_snapshot_fingerprint"] = contentFingerprint
		if dataset == datasetID {
			item.Attributes["missing_complete_snapshot_count"] = "0"
			delete(item.Attributes, "last_missing_snapshot_id")
			delete(item.Attributes, "last_missing_snapshot_date")
		}
		return item
	}
	subjectIDs := make([]string, 0, len(present))
	for subjectID := range present {
		subjectIDs = append(subjectIDs, subjectID)
	}
	sort.Strings(subjectIDs)
	for _, subjectID := range subjectIDs {
		setPresent(datasetID, "record", subjectID)
		if targetDatasetID != "" && targetDatasetID != datasetID {
			setPresent(targetDatasetID, "normal", subjectID)
		}
	}
	location, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		location = time.FixedZone("CST", 8*60*60)
	}
	missingDate := snapshot.FetchedAt.In(location).Format("2006-01-02")
	for _, membership := range existing {
		if membership == nil || !strings.EqualFold(membership.GetStatus(), "active") {
			continue
		}
		if _, ok := present[membership.GetSubjectId()]; ok {
			continue
		}
		key := membership.GetDatasetId() + "\x00" + membership.GetSubjectId()
		updated := desired[key]
		updated.Attributes = cloneAttributes(updated.GetAttributes())
		missingCount, _ := strconv.Atoi(updated.Attributes["missing_complete_snapshot_count"])
		if updated.Attributes["last_missing_snapshot_date"] != missingDate {
			missingCount++
		}
		updated.Attributes["missing_complete_snapshot_count"] = strconv.Itoa(missingCount)
		updated.Attributes["last_missing_snapshot_id"] = snapshot.SnapshotID
		updated.Attributes["last_missing_snapshot_date"] = missingDate
		updated.Attributes["active_instrument_set_version"] = snapshot.SnapshotID
		updated.Attributes["active_instrument_set_fetched_at"] = snapshot.FetchedAt.UTC().Format(time.RFC3339Nano)
		updated.Attributes["instrument_snapshot_fingerprint"] = contentFingerprint
		if strings.EqualFold(spaceID, "crypto") {
			// Binance snapshots are complete exchange snapshots. Retaining a
			// missing symbol as active would make every Timer reconciliation fail
			// for the whole fleet, so remove it from the active set immediately.
			updated.Status = "disabled"
			if targetDatasetID != "" && targetDatasetID != datasetID {
				targetKey := targetDatasetID + "\x00" + membership.GetSubjectId()
				target := desired[targetKey]
				if target != nil {
					target.Attributes = cloneAttributes(target.GetAttributes())
					target.Attributes["active_instrument_set_version"] = snapshot.SnapshotID
					target.Attributes["active_instrument_set_fetched_at"] = snapshot.FetchedAt.UTC().Format(time.RFC3339Nano)
					target.Attributes["instrument_snapshot_fingerprint"] = contentFingerprint
					target.Status = "disabled"
				}
			}
			continue
		}
		if targetDatasetID != "" && targetDatasetID != datasetID {
			targetKey := targetDatasetID + "\x00" + membership.GetSubjectId()
			target := desired[targetKey]
			if target != nil {
				target.Attributes = cloneAttributes(target.GetAttributes())
				target.Attributes["active_instrument_set_version"] = snapshot.SnapshotID
				target.Attributes["active_instrument_set_fetched_at"] = snapshot.FetchedAt.UTC().Format(time.RFC3339Nano)
				target.Attributes["instrument_snapshot_fingerprint"] = contentFingerprint
			}
		}
		if missingCount >= 2 {
			updated.Status = "disabled"
			if targetDatasetID != "" && targetDatasetID != datasetID {
				targetKey := targetDatasetID + "\x00" + membership.GetSubjectId()
				target := desired[targetKey]
				if target == nil {
					target = proto.Clone(updated).(*storagepb.DatasetSubject)
				}
				target.DatasetId = targetDatasetID
				target.SubjectRole = "normal"
				target.Status = "disabled"
				desired[targetKey] = target
			}
		}
	}
	keys := make([]string, 0, len(desired))
	for key := range desired {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	bindings := make([]*storagepb.DatasetSubject, 0, len(keys))
	for _, key := range keys {
		bindings = append(bindings, desired[key])
	}
	return bindings
}

func instrumentSubjectIDs(sets ...[]*storagepb.DatasetSubject) map[string]struct{} {
	known := make(map[string]struct{})
	for _, set := range sets {
		for _, membership := range set {
			if membership != nil && strings.TrimSpace(membership.GetSubjectId()) != "" {
				known[membership.GetSubjectId()] = struct{}{}
			}
		}
	}
	return known
}

func registeredSubjectIDs(symbols []*storagepb.SubjectSymbol) map[string]struct{} {
	known := make(map[string]struct{}, len(symbols))
	for _, symbol := range symbols {
		if symbol == nil || !strings.EqualFold(strings.TrimSpace(symbol.GetStatus()), "active") || strings.TrimSpace(symbol.GetExternalSymbol()) == "" {
			continue
		}
		if subjectID := strings.TrimSpace(symbol.GetSubjectId()); subjectID != "" {
			known[subjectID] = struct{}{}
		}
	}
	return known
}

func newestInstrumentSnapshotAt(sets ...[]*storagepb.DatasetSubject) time.Time {
	var newest time.Time
	for _, set := range sets {
		for _, membership := range set {
			if membership == nil || !strings.EqualFold(strings.TrimSpace(membership.GetStatus()), "active") {
				continue
			}
			value := strings.TrimSpace(membership.GetAttributes()["active_instrument_set_fetched_at"])
			if value == "" {
				continue
			}
			fetchedAt, err := time.Parse(time.RFC3339Nano, value)
			if err == nil && fetchedAt.After(newest) {
				newest = fetchedAt.UTC()
			}
		}
	}
	return newest
}

func instrumentRegistration(spaceID, dataSourceID string, snapshot marketdata.InstrumentSnapshot, instrument marketdata.Instrument) *storagepb.RegisterDataSubjectReq {
	return instrumentRegistrationWithMetadata(spaceID, dataSourceID, "stock", "CN", "CNY", "Asia/Shanghai", "equity", snapshot, instrument)
}

func (p *InstrumentPipeline) instrumentRegistration(spaceID, dataSourceID string, snapshot marketdata.InstrumentSnapshot, instrument marketdata.Instrument) *storagepb.RegisterDataSubjectReq {
	marketID := firstNonEmptyString(p.MarketID, StockCNSpaceID)
	subjectType, subjectMarket, currency, timezone, instrumentType := p.SubjectType, p.SubjectMarket, p.Currency, p.Timezone, p.InstrumentType
	if strings.EqualFold(marketID, StockCNSpaceID) {
		subjectType = firstNonEmptyString(subjectType, "stock")
		subjectMarket = firstNonEmptyString(subjectMarket, "CN")
		currency = firstNonEmptyString(currency, "CNY")
		timezone = firstNonEmptyString(timezone, "Asia/Shanghai")
		instrumentType = firstNonEmptyString(instrumentType, "equity")
	} else {
		subjectType = firstNonEmptyString(subjectType, "instrument")
		subjectMarket = firstNonEmptyString(subjectMarket, marketID)
		timezone = firstNonEmptyString(timezone, "UTC")
		instrumentType = firstNonEmptyString(instrumentType, "spot")
	}
	return instrumentRegistrationWithMetadata(spaceID, dataSourceID, subjectType, subjectMarket, currency, timezone, instrumentType, snapshot, instrument)
}

func instrumentRegistrationWithMetadata(spaceID, dataSourceID, subjectType, subjectMarket, currency, timezone, instrumentType string, snapshot marketdata.InstrumentSnapshot, instrument marketdata.Instrument) *storagepb.RegisterDataSubjectReq {
	attributes := map[string]string{"exchange": instrument.Exchange, "instrument_type": instrumentType, "provider_symbol": instrument.ProviderSymbol, "snapshot_id": snapshot.SnapshotID, "source_provider": snapshot.SourceProvider}
	status := strings.TrimSpace(instrument.Status)
	if status == "" {
		status = "active"
	}
	return &storagepb.RegisterDataSubjectReq{SpaceId: spaceID, DataSourceId: dataSourceID, ExternalSymbol: instrument.ProviderSymbol, Subject: &storagepb.Subject{SpaceId: spaceID, SubjectId: instrument.SubjectID, SubjectType: subjectType, Name: firstNonEmptyString(instrument.Name, instrument.SubjectID), Market: subjectMarket, Currency: currency, Timezone: timezone, Status: status, Attributes: attributes}}
}

func instrumentRecordRow(spaceID, datasetID string, snapshot marketdata.InstrumentSnapshot, instrument marketdata.Instrument) *storagepb.RowFieldUpsert {
	fields := make([]*storagepb.FieldValue, 0, 9)
	if strings.EqualFold(strings.TrimSpace(spaceID), "crypto") {
		fields = append(fields,
			stringValue("symbol", firstNonEmptyString(instrument.CanonicalSymbol, instrument.SubjectID)),
			stringValue("external_symbol", instrument.ProviderSymbol),
			stringValue("base_asset", instrument.BaseAsset),
			stringValue("quote_asset", instrument.QuoteAsset),
			stringValue("status", firstNonEmptyString(instrument.Status, "active")),
		)
		fields = appendOptionalInstrumentNumber(fields, "min_qty", instrument.MinQty)
		fields = appendOptionalInstrumentNumber(fields, "max_qty", instrument.MaxQty)
		fields = appendOptionalInstrumentNumber(fields, "tick_size", instrument.TickSize)
		fields = appendOptionalInstrumentNumber(fields, "lot_size", instrument.LotSize)
	} else {
		fields = append(fields,
			stringValue("security_code", instrument.SubjectID),
			stringValue("provider_symbol", instrument.ProviderSymbol),
			stringValue("exchange", instrument.Exchange),
			stringValue("instrument_name", instrument.Name),
			stringValue("instrument_status", instrument.Status),
			stringValue("snapshot_id", snapshot.SnapshotID),
			stringValue("source_provider", snapshot.SourceProvider),
			timeValue("fetched_at", snapshot.FetchedAt),
		)
	}
	return &storagepb.RowFieldUpsert{Key: &storagepb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: instrument.SubjectID, Version: snapshot.SnapshotID}}}, Fields: fields}
}

func appendOptionalInstrumentNumber(fields []*storagepb.FieldValue, field, raw string) []*storagepb.FieldValue {
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil {
		return fields
	}
	return append(fields, doubleValue(field, value))
}

func uniqueProviders(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func filterAbsentMemberships(memberships []*storagepb.DatasetSubject, present map[string]marketdata.Instrument) []*storagepb.DatasetSubject {
	filtered := make([]*storagepb.DatasetSubject, 0, len(memberships))
	for _, membership := range memberships {
		if membership == nil {
			continue
		}
		if _, ok := present[membership.GetSubjectId()]; ok {
			continue
		}
		filtered = append(filtered, membership)
	}
	return filtered
}

func cloneAttributes(input map[string]string) map[string]string {
	output := make(map[string]string, len(input)+2)
	for key, value := range input {
		output[key] = value
	}
	return output
}

func cloneCounts(input map[string]int) map[string]int {
	output := make(map[string]int, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
