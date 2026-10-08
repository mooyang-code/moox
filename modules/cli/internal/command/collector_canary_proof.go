package command

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	collectorpb "github.com/mooyang-code/moox/modules/collector/proto/collectorgen"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/marketcalendar"
	metricsreport "github.com/mooyang-code/moox/packages/report"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
	"google.golang.org/protobuf/encoding/protojson"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/codec"
)

const (
	collectorCanaryInventoryPageSize = 100
	collectorCanaryInventoryMaxItems = 1000
	collectorCanaryCandidatePeriods  = 24
	collectorCanarySettleDelay       = 5 * time.Second
	collectorCanaryProofTimeout      = 90 * time.Second
	collectorCanaryProofPollInterval = time.Second
	collectorCanaryReservationTTL    = time.Hour
	collectorCanaryHistorySafetyGap  = time.Hour
)

var errCollectorCanaryPeriodOccupied = errors.New("canary period is already reserved or terminal")

type collectorCanaryInventoryReader interface {
	GetTaskResultInventory(context.Context, *collectorpb.GetTaskResultInventoryReq, ...client.Option) (*collectorpb.GetTaskResultInventoryRsp, error)
}

type collectorCanaryPrimaryReader interface {
	EnsureDatasetPeriod(context.Context, *storagepb.PrimaryEnsureDatasetPeriodReq, ...client.Option) (*storagepb.PrimaryEnsureDatasetPeriodRsp, error)
	GetDatasetPeriodStatus(context.Context, *storagepb.PrimaryGetDatasetPeriodStatusReq, ...client.Option) (*storagepb.PrimaryGetDatasetPeriodStatusRsp, error)
	ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
}

type collectorCanaryViewReader interface {
	QueryTimeSeriesRows(context.Context, *storagepb.QueryTimeSeriesRowsReq, ...client.Option) (*storagepb.QueryTimeSeriesRowsRsp, error)
}

type collectorCanaryAccess struct {
	inventory   collectorCanaryInventoryReader
	primary     collectorCanaryPrimaryReader
	view        collectorCanaryViewReader
	periodAuth  *commonpb.AuthInfo
	primaryAuth *commonpb.AuthInfo
	viewAuth    *commonpb.AuthInfo
}

type collectorSCFCanaryProof struct {
	entry         *collectorpb.TaskResultInventoryEntry
	period        time.Time
	interval      time.Duration
	deadlineAt    int64
	reservationID string
	access        collectorCanaryAccess
	clock         func() time.Time
}

func (p *collectorSCFCanaryProof) currentTime() time.Time {
	if p != nil && p.clock != nil {
		return p.clock().UTC()
	}
	return time.Now().UTC()
}

func (p *collectorSCFCanaryProof) revalidateTaskBinding(ctx context.Context) error {
	if p == nil || p.entry == nil || p.access.inventory == nil {
		return errors.New("SCF canary verification incomplete: live Collector task inventory is unavailable")
	}
	entries, err := loadCollectorCanaryInventory(ctx, p.access.inventory, p.entry.GetSpaceId())
	if err != nil {
		return fmt.Errorf("refresh Collector task-owned canary inventory: %w", err)
	}
	var current *collectorpb.TaskResultInventoryEntry
	for _, entry := range entries {
		if entry != nil && entry.GetTaskId() == p.entry.GetTaskId() {
			if current != nil {
				return fmt.Errorf("canary task %q appears more than once in refreshed inventory", p.entry.GetTaskId())
			}
			current = entry
		}
	}
	if current == nil || current.GetSpaceId() != p.entry.GetSpaceId() || current.GetEnabled() || !current.GetOwnershipVerified() ||
		!current.GetCanaryCandidateAvailable() || !sameCollectorCanaryBinding(p.entry, current) {
		return fmt.Errorf("canary task %q changed or is no longer a disabled, ownership-verified single-series task; refusing to invoke", p.entry.GetTaskId())
	}
	return nil
}

func sameCollectorCanaryBinding(left, right *collectorpb.TaskResultInventoryEntry) bool {
	if left == nil || right == nil {
		return false
	}
	return left.GetSpaceId() == right.GetSpaceId() && left.GetTaskId() == right.GetTaskId() &&
		left.GetDatasetId() == right.GetDatasetId() && left.GetViewId() == right.GetViewId() &&
		left.GetFrequency() == right.GetFrequency() && left.GetMarketId() == right.GetMarketId() &&
		left.GetSubjectId() == right.GetSubjectId() && left.GetProviderSymbol() == right.GetProviderSymbol() &&
		left.GetProvider() == right.GetProvider() && left.GetSourceId() == right.GetSourceId() &&
		left.GetMarketType() == right.GetMarketType() && left.GetSeriesTag() == right.GetSeriesTag() &&
		left.GetSeriesIndex() == right.GetSeriesIndex() && left.GetSeriesHash() == right.GetSeriesHash() &&
		left.GetExpectedCount() == right.GetExpectedCount() && slices.Equal(left.GetOutputFields(), right.GetOutputFields())
}

// collectorHTTPInventoryReader reads the task-result inventory through the
// service gateway's HTTP route, the same path Monitor uses. CollectMgr only
// serves HTTP, so the native tRPC gateway cannot reach it.
type collectorHTTPInventoryReader struct {
	control *adminclient.Client
}

func (r collectorHTTPInventoryReader) GetTaskResultInventory(ctx context.Context, req *collectorpb.GetTaskResultInventoryReq, _ ...client.Option) (*collectorpb.GetTaskResultInventoryRsp, error) {
	if r.control == nil {
		return nil, errors.New("Collector inventory control client is not configured")
	}
	body, err := protojson.MarshalOptions{UseProtoNames: true}.Marshal(req)
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := r.control.CallJSON(ctx, http.MethodPost, "/api/admin/collectmgr/GetTaskResultInventory", json.RawMessage(body), &raw); err != nil {
		return nil, err
	}
	rsp := &collectorpb.GetTaskResultInventoryRsp{}
	if err := (protojson.UnmarshalOptions{DiscardUnknown: true}).Unmarshal(raw, rsp); err != nil {
		return nil, fmt.Errorf("decode Collector inventory: %w", err)
	}
	return rsp, nil
}

func newCollectorCanaryAccess(fetcher *setupconfig.SCFFetcherSpace, inventory collectorCanaryInventoryReader, storageTarget, storageNode string, trust collectorSCFTrustMaterial) (collectorCanaryAccess, error) {
	if fetcher == nil {
		return collectorCanaryAccess{}, errors.New("SCF canary requires manifest task ownership")
	}
	if inventory == nil {
		return collectorCanaryAccess{}, errors.New("Collector inventory reader is required")
	}
	if strings.TrimSpace(storageTarget) == "" || strings.TrimSpace(storageNode) == "" {
		return collectorCanaryAccess{}, errors.New("Storage proof gateway target and node are required")
	}
	if strings.TrimSpace(trust.StoragePrimaryAuthSecret) == "" || strings.TrimSpace(trust.StorageViewAuthSecret) == "" {
		return collectorCanaryAccess{}, errors.New("Storage Primary and View proof credentials are required")
	}

	storageOptions := gatewayauth.NewTRPCClientOptions(storageTarget, storageNode, gatewayauth.Credentials{
		KeyID: "collector", Caller: "collector", Secret: trust.CollectorServiceKey,
	})
	storageOptions = append(storageOptions, client.WithTimeout(5*time.Second))
	primaryReadAppID := "scf-market-canary"
	viewReadAppID := "scf-market-canary"
	return collectorCanaryAccess{
		inventory: inventory,
		primary:   storagepb.NewPrimaryStoreClientProxy(storageOptions...),
		view:      storagepb.NewDataViewClientProxy(storageOptions...),
		periodAuth: &commonpb.AuthInfo{
			AppId: "collector", AppKey: mooxsecurity.HMACSHA256Hex(trust.StoragePrimaryAuthSecret, []byte("collector")),
		},
		primaryAuth: &commonpb.AuthInfo{
			AppId: primaryReadAppID, AppKey: mooxsecurity.HMACSHA256Hex(trust.StoragePrimaryAuthSecret, []byte(primaryReadAppID)),
		},
		viewAuth: &commonpb.AuthInfo{
			AppId: viewReadAppID, AppKey: mooxsecurity.HMACSHA256Hex(trust.StorageViewAuthSecret, []byte(viewReadAppID)),
		},
	}, nil
}

func prepareCollectorSCFCanaryProof(ctx context.Context, fetcher *setupconfig.SCFFetcherSpace, access collectorCanaryAccess, now time.Time) (*collectorSCFCanaryProof, error) {
	return prepareCollectorSCFCanaryProofWithClock(ctx, fetcher, access, now, time.Now)
}

func prepareCollectorSCFCanaryProofWithClock(ctx context.Context, fetcher *setupconfig.SCFFetcherSpace, access collectorCanaryAccess, now time.Time, clock func() time.Time) (*collectorSCFCanaryProof, error) {
	if err := validateCollectorSCFCanaryTaskBinding(fetcher); err != nil {
		return nil, err
	}
	if clock == nil {
		clock = time.Now
	}
	if access.inventory == nil || access.primary == nil || access.view == nil || access.periodAuth == nil || access.primaryAuth == nil || access.viewAuth == nil {
		return nil, errors.New("SCF canary verification contract is unavailable: Collector inventory, Primary and View readers are required before account registration")
	}
	entries, err := loadCollectorCanaryInventory(ctx, access.inventory, fetcher.SpaceID)
	if err != nil {
		return nil, fmt.Errorf("read Collector task-owned canary inventory: %w", err)
	}
	entry, err := selectCollectorCanaryTask(entries, fetcher)
	if err != nil {
		return nil, err
	}
	interval, err := metricsreport.ParseDatasetFrequency(entry.GetFrequency())
	if err != nil || interval <= 0 {
		return nil, fmt.Errorf("canary task frequency %q is invalid", entry.GetFrequency())
	}
	periods, err := collectorSCFCanaryPeriods(entry, now, collectorCanaryCandidatePeriods)
	if err != nil {
		return nil, err
	}
	for _, period := range periods {
		reservationID, err := newCollectorCanaryReservationID()
		if err != nil {
			return nil, fmt.Errorf("create unique canary period reservation: %w", err)
		}
		proof := &collectorSCFCanaryProof{entry: entry, period: period.UTC(), interval: interval, reservationID: reservationID, access: access, clock: clock}
		free, probeErr := proof.periodIsUnused(ctx)
		if probeErr != nil {
			return nil, fmt.Errorf("preflight canary period %s: %w", period.Format(time.RFC3339), probeErr)
		}
		if !free {
			continue
		}
		proof.deadlineAt = now.UTC().Add(collectorCanaryReservationTTL).Unix()
		if reserveErr := proof.ensurePeriod(ctx); reserveErr != nil {
			if errors.Is(reserveErr, errCollectorCanaryPeriodOccupied) {
				continue
			}
			return nil, fmt.Errorf("reserve canary period %s before publication side effects: %w", period.Format(time.RFC3339), reserveErr)
		}
		return proof, nil
	}
	return nil, fmt.Errorf("SCF canary verification contract is unavailable: no unused task-owned period could be exclusively reserved among %d bounded candidates before account registration", len(periods))
}

func newCollectorCanaryReservationID() (string, error) {
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	return "scf-release-" + hex.EncodeToString(random[:]), nil
}

func validateCollectorSCFCanaryTaskBinding(fetcher *setupconfig.SCFFetcherSpace) error {
	if fetcher == nil || strings.TrimSpace(fetcher.CanaryTaskID) == "" {
		return errors.New("SCF canary verification contract is unavailable: configure scf_fetcher.spaces.canary_task_id for a dedicated disabled, single-series task before account registration")
	}
	if strings.TrimSpace(fetcher.SpaceID) == "" {
		return errors.New("SCF canary verification contract is unavailable: manifest Space identity is required before account registration")
	}
	return nil
}

func validateCollectorSCFCanaryInvokePlan(fetcher *setupconfig.SCFFetcherSpace, limits setupconfig.TencentSCFLimits, regionOverride string) error {
	if err := validateCollectorSCFCanaryTaskBinding(fetcher); err != nil {
		return err
	}
	for _, region := range fetcher.Regions {
		if regionOverride != "" && !strings.EqualFold(strings.TrimSpace(region.Region), strings.TrimSpace(regionOverride)) {
			continue
		}
		for _, shard := range setupconfig.SpaceRegionNamespaceShards(*fetcher, region, limits) {
			if shard.Invokes > 0 {
				return nil
			}
		}
	}
	return errors.New("SCF canary verification contract is unavailable: publication plan has no enabled Invoke shard; Timer-only plans cannot bypass business proof before account registration")
}

func requiresIsolatedCollectorReleaseCanary(spaceID string) bool {
	switch strings.ToLower(strings.TrimSpace(spaceID)) {
	case "crypto", "stockcn":
		return true
	default:
		return false
	}
}

func parseCollectorRegionPackageIDs(values []string, fetcher *setupconfig.SCFFetcherSpace) (map[string]string, error) {
	if fetcher == nil {
		return nil, errors.New("stockcn manifest is required to validate region package identities")
	}
	expectedRegions := make(map[string]struct{})
	for _, region := range fetcher.Regions {
		if region.Enabled && region.FunctionCount > 0 && !fetcher.IsRegionBlacklisted(region.Region) {
			expectedRegions[strings.ToLower(strings.TrimSpace(region.Region))] = struct{}{}
		}
	}
	packageIDs := make(map[string]string, len(values))
	for _, value := range values {
		region, packageID, ok := strings.Cut(strings.TrimSpace(value), "=")
		region = strings.ToLower(strings.TrimSpace(region))
		packageID = strings.TrimSpace(packageID)
		if !ok || region == "" || packageID == "" {
			return nil, fmt.Errorf("--region-package-id must use REGION=PACKAGE_ID")
		}
		if _, exists := expectedRegions[region]; !exists {
			return nil, fmt.Errorf("--region-package-id names inactive or unknown stockcn region %q", region)
		}
		if _, exists := packageIDs[region]; exists {
			return nil, fmt.Errorf("--region-package-id repeats stockcn region %q", region)
		}
		packageIDs[region] = packageID
	}
	for region := range expectedRegions {
		if packageIDs[region] == "" {
			return nil, fmt.Errorf("--region-package-id is required for enabled stockcn region %q", region)
		}
	}
	if len(expectedRegions) == 0 {
		return nil, errors.New("stockcn manifest has no enabled, non-blacklisted regions to activate")
	}
	return packageIDs, nil
}

func validateStockCNPackageIdentity(node adminclient.CloudNode, region, version string, packageIDs map[string]string) error {
	expectedPackageID := packageIDs[strings.ToLower(strings.TrimSpace(region))]
	if expectedPackageID == "" {
		return fmt.Errorf("stockcn region %s has no exact package identity in activation request", region)
	}
	if node.PackageID != expectedPackageID {
		return fmt.Errorf("stockcn node %s in region %s is on package %q; expected exact package %q", node.NodeID, region, node.PackageID, expectedPackageID)
	}
	if version != "" && !strings.Contains(node.PackageID, version) {
		return fmt.Errorf("stockcn node %s in region %s is on package %q, which does not contain requested version %q", node.NodeID, region, node.PackageID, version)
	}
	return nil
}

func requireCollectorFleetPackageID(nodes []adminclient.CloudNode, expectedPackageID string) error {
	expectedPackageID = strings.TrimSpace(expectedPackageID)
	if expectedPackageID == "" {
		return errors.New("candidate SCF package identity is required")
	}
	for _, node := range nodes {
		if strings.TrimSpace(node.NodeID) == "" || node.PackageID != expectedPackageID {
			return fmt.Errorf("SCF node %q is on package %q; expected exact candidate package %q", node.NodeID, node.PackageID, expectedPackageID)
		}
	}
	return nil
}

func collectorSCFReleaseCanaryOptions(base collectorPublishOptions, fetcher *setupconfig.SCFFetcherSpace, region setupconfig.SCFFetcherRegion, limits setupconfig.TencentSCFLimits) (collectorPublishOptions, error) {
	if fetcher == nil || !requiresIsolatedCollectorReleaseCanary(fetcher.SpaceID) {
		return collectorPublishOptions{}, errors.New("dedicated release canary function is only defined for Crypto and StockCN Invoke pools")
	}
	namespace, err := setupconfig.SpaceRegionReleaseCanaryNamespace(*fetcher, region, limits)
	if err != nil {
		return collectorPublishOptions{}, err
	}
	base.Region = region.Region
	base.Namespace = namespace
	base.TriggerType = "invoke"
	base.NodeCount = 1
	base.IndexOffset = 0
	base.FunctionNamePrefix = strings.TrimSuffix(defaultFlag(base.FunctionNamePrefix, fetcher.FunctionPrefix), "-") + "-release-canary"
	if base.canaryProof != nil && base.canaryProof.reservationID != "" {
		base.FunctionNamePrefix, err = uniqueCollectorCanaryFunctionPrefix(base.canaryProof.reservationID, region.Region)
		if err != nil {
			return collectorPublishOptions{}, err
		}
	}
	return base, nil
}

func uniqueCollectorCanaryFunctionPrefix(reservationID, region string) (string, error) {
	const reservationPrefix = "scf-release-"
	reservationID = strings.TrimSpace(reservationID)
	if !strings.HasPrefix(reservationID, reservationPrefix) {
		return "", errors.New("SCF canary reservation identity is invalid")
	}
	token := strings.TrimPrefix(reservationID, reservationPrefix)
	if len(token) != 32 {
		return "", errors.New("SCF canary reservation identity exceeds function-name limit")
	}
	if _, err := hex.DecodeString(token); err != nil {
		return "", errors.New("SCF canary reservation identity contains an invalid function-name character")
	}
	region = strings.TrimSpace(region)
	if region == "" {
		return "", errors.New("SCF canary region is required for function-name validation")
	}
	// CloudNode appends "-<region>-<index>" to this prefix. Preserve the full
	// 128-bit reservation token while keeping the resulting Tencent SCF name
	// within its 60-character limit for the selected region.
	prefix := "r" + token
	if len(prefix)+len("-"+region+"-0") > 60 {
		return "", errors.New("SCF canary function name exceeds 60-character limit")
	}
	return prefix, nil
}

func selectCollectorSCFCanaryRegion(fetcher *setupconfig.SCFFetcherSpace, limits setupconfig.TencentSCFLimits, regionOverride, preferredRegion string) (setupconfig.SCFFetcherRegion, error) {
	if err := validateCollectorSCFCanaryInvokePlan(fetcher, limits, regionOverride); err != nil {
		return setupconfig.SCFFetcherRegion{}, err
	}
	regions := append([]setupconfig.SCFFetcherRegion(nil), fetcher.Regions...)
	if strings.TrimSpace(regionOverride) != "" {
		selected := regions[:0]
		for _, region := range regions {
			if strings.EqualFold(strings.TrimSpace(region.Region), strings.TrimSpace(regionOverride)) {
				selected = append(selected, region)
				break
			}
		}
		regions = selected
	} else if strings.TrimSpace(preferredRegion) != "" {
		regions = orderCollectorPublishRegions(regions, preferredRegion, true)
	}
	for _, region := range regions {
		for _, shard := range setupconfig.SpaceRegionNamespaceShards(*fetcher, region, limits) {
			if shard.Invokes > 0 {
				return region, nil
			}
		}
	}
	return setupconfig.SCFFetcherRegion{}, errors.New("SCF canary verification contract is unavailable: publication plan has no enabled Invoke shard")
}

func loadCollectorCanaryInventory(ctx context.Context, reader collectorCanaryInventoryReader, spaceID string) ([]*collectorpb.TaskResultInventoryEntry, error) {
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return nil, errors.New("Collector inventory Space identity is required")
	}
	var entries []*collectorpb.TaskResultInventoryEntry
	var snapshotID, observedAt string
	expectedTotal := -1
	for page := uint32(1); ; page++ {
		if len(entries) >= collectorCanaryInventoryMaxItems || page > collectorCanaryInventoryMaxItems/collectorCanaryInventoryPageSize+1 {
			return nil, fmt.Errorf("inventory exceeded %d entries", collectorCanaryInventoryMaxItems)
		}
		requestCtx, msg := codec.WithCloneMessage(ctx)
		metadata := msg.ClientMetaData().Clone()
		if metadata == nil {
			metadata = codec.MetaData{}
		}
		for key, value := range codec.Message(ctx).ClientMetaData() {
			metadata[key] = value
		}
		metadata["space_id"] = []byte(spaceID)
		msg.WithClientMetaData(metadata)
		rsp, err := reader.GetTaskResultInventory(requestCtx, &collectorpb.GetTaskResultInventoryReq{
			SpaceId: spaceID, Page: &commonpb.Page{Page: page, Size: collectorCanaryInventoryPageSize}, SnapshotId: snapshotID,
		})
		if err != nil {
			return nil, fmt.Errorf("page %d RPC: %w", page, err)
		}
		if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != collectorpb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("page %d returned no successful response", page)
		}
		pageInfo := rsp.GetPage()
		if pageInfo == nil || pageInfo.GetPage() != page || pageInfo.GetSize() != collectorCanaryInventoryPageSize {
			return nil, fmt.Errorf("page %d omitted or changed pagination metadata", page)
		}
		if page == 1 {
			if strings.TrimSpace(rsp.GetSnapshotId()) == "" || pageInfo.GetTotal() > collectorCanaryInventoryMaxItems {
				return nil, errors.New("inventory snapshot identity or total is invalid")
			}
			if _, parseErr := time.Parse(time.RFC3339Nano, strings.TrimSpace(rsp.GetObservedAt())); parseErr != nil {
				return nil, fmt.Errorf("inventory observed_at is invalid: %w", parseErr)
			}
			snapshotID, observedAt, expectedTotal = rsp.GetSnapshotId(), rsp.GetObservedAt(), int(pageInfo.GetTotal())
		} else if rsp.GetSnapshotId() != snapshotID || rsp.GetObservedAt() != observedAt || int(pageInfo.GetTotal()) != expectedTotal {
			return nil, fmt.Errorf("page %d does not belong to the first-page snapshot", page)
		}
		if len(rsp.GetEntries()) > collectorCanaryInventoryPageSize || pageInfo.GetHasMore() && len(rsp.GetEntries()) == 0 {
			return nil, fmt.Errorf("page %d has an invalid bounded entry count", page)
		}
		entries = append(entries, rsp.GetEntries()...)
		if len(entries) > collectorCanaryInventoryMaxItems || len(entries) > expectedTotal {
			return nil, errors.New("inventory exceeds its declared bound")
		}
		if !pageInfo.GetHasMore() {
			if len(entries) != expectedTotal {
				return nil, fmt.Errorf("inventory is incomplete: got %d of %d", len(entries), expectedTotal)
			}
			return entries, nil
		}
	}
}

func selectCollectorCanaryTask(entries []*collectorpb.TaskResultInventoryEntry, fetcher *setupconfig.SCFFetcherSpace) (*collectorpb.TaskResultInventoryEntry, error) {
	wantedTask := strings.TrimSpace(fetcher.CanaryTaskID)
	var selected *collectorpb.TaskResultInventoryEntry
	for _, entry := range entries {
		if entry == nil || strings.TrimSpace(entry.GetTaskId()) != wantedTask {
			continue
		}
		if selected != nil {
			return nil, fmt.Errorf("SCF canary task %q appears more than once in the Collector inventory", wantedTask)
		}
		selected = entry
	}
	if selected == nil {
		return nil, fmt.Errorf("SCF canary verification contract is unavailable: task %q is absent from Collector inventory", wantedTask)
	}
	if selected.GetSpaceId() != strings.TrimSpace(fetcher.SpaceID) || selected.GetTaskId() != wantedTask || selected.GetEnabled() {
		return nil, fmt.Errorf("SCF canary task %q must be disabled and belong to Space %q", wantedTask, fetcher.SpaceID)
	}
	if !selected.GetCanaryCandidateAvailable() || !selected.GetOwnershipVerified() || selected.GetDatasetId() == "" || selected.GetViewId() == "" ||
		selected.GetFrequency() == "" || selected.GetSubjectId() == "" || selected.GetProviderSymbol() == "" || selected.GetProvider() == "" ||
		selected.GetSourceId() == "" || selected.GetMarketType() == "" || selected.GetSeriesTag() == "" || selected.GetSeriesIndex() != 0 ||
		selected.GetSeriesHash() == "" || selected.GetExpectedCount() != 1 || len(selected.GetOutputFields()) == 0 {
		return nil, fmt.Errorf("SCF canary task %q does not have an ownership-verified single-series Dataset/View binding", wantedTask)
	}
	if expected := strings.TrimSpace(fetcher.MarketID); expected != "" && !strings.EqualFold(selected.GetMarketId(), expected) {
		return nil, fmt.Errorf("SCF canary task %q market %q does not match manifest market %q", wantedTask, selected.GetMarketId(), expected)
	}
	if expected := strings.TrimSpace(fetcher.ProviderID); expected != "" && !strings.EqualFold(selected.GetProvider(), expected) {
		return nil, fmt.Errorf("SCF canary task %q provider %q does not match manifest provider %q", wantedTask, selected.GetProvider(), expected)
	}
	if expected := strings.TrimSpace(fetcher.SourceID); expected != "" && !strings.EqualFold(selected.GetSourceId(), expected) {
		return nil, fmt.Errorf("SCF canary task %q source %q does not match manifest source %q", wantedTask, selected.GetSourceId(), expected)
	}
	if expected := strings.TrimSpace(fetcher.InstrumentType); expected != "" && !strings.EqualFold(selected.GetMarketType(), expected) {
		return nil, fmt.Errorf("SCF canary task %q instrument %q does not match manifest instrument %q", wantedTask, selected.GetMarketType(), expected)
	}
	return selected, nil
}

func collectorSCFCanaryPeriods(entry *collectorpb.TaskResultInventoryEntry, now time.Time, limit int) ([]time.Time, error) {
	if entry == nil || limit < 1 {
		return nil, errors.New("canary period identity and positive candidate bound are required")
	}
	interval, err := metricsreport.ParseDatasetFrequency(entry.GetFrequency())
	if err != nil || interval <= 0 {
		return nil, fmt.Errorf("canary frequency %q is invalid", entry.GetFrequency())
	}
	if strings.EqualFold(entry.GetMarketId(), "stockcn") || strings.EqualFold(entry.GetCalendarId(), "cn_stock") {
		return collectorStockCNCanaryPeriods(entry, now, limit, interval)
	}
	first := now.UTC().Add(-collectorCanarySettleDelay).Truncate(interval).Add(-interval)
	periods := make([]time.Time, 0, limit)
	for i := 0; i < limit; i++ {
		periods = append(periods, first.Add(-time.Duration(i)*interval))
	}
	return periods, nil
}

func collectorStockCNCanaryPeriods(entry *collectorpb.TaskResultInventoryEntry, now time.Time, limit int, interval time.Duration) ([]time.Time, error) {
	if interval != time.Minute || !strings.EqualFold(strings.TrimSpace(entry.GetCalendarId()), "cn_stock") || strings.TrimSpace(entry.GetTimezone()) == "" || len(entry.GetSessions()) == 0 {
		return nil, errors.New("StockCN canary requires the cn_stock calendar, timezone, sessions and 1m frequency")
	}
	maxLookback, err := stockCNCanaryProviderHistory(entry.GetProvider())
	if err != nil {
		return nil, err
	}
	maxAge := maxLookback - collectorCanaryReservationTTL - collectorCanaryHistorySafetyGap
	if maxAge <= 0 {
		return nil, fmt.Errorf("StockCN canary provider %q has no usable history window after reservation and safety margins", entry.GetProvider())
	}
	location, err := time.LoadLocation(entry.GetTimezone())
	if err != nil {
		return nil, fmt.Errorf("load canary timezone: %w", err)
	}
	calendar, err := marketcalendar.Load("cn_stock")
	if err != nil {
		return nil, fmt.Errorf("load canary trading calendar: %w", err)
	}
	sessions := make([][2]int, 0, len(entry.GetSessions()))
	for _, raw := range entry.GetSessions() {
		start, end, ok := strings.Cut(strings.TrimSpace(raw), "-")
		if !ok {
			return nil, fmt.Errorf("invalid StockCN session %q", raw)
		}
		startTime, startErr := time.Parse("15:04", start)
		endTime, endErr := time.Parse("15:04", end)
		if startErr != nil || endErr != nil || !startTime.Before(endTime) {
			return nil, fmt.Errorf("invalid StockCN session %q", raw)
		}
		sessions = append(sessions, [2]int{startTime.Hour()*60 + startTime.Minute(), endTime.Hour()*60 + endTime.Minute()})
	}
	nowLocal := now.In(location)
	cursor := nowLocal.Add(-collectorCanarySettleDelay).Truncate(time.Minute).Add(-time.Minute)
	maxScan := 31 * 24 * 60
	periods := make([]time.Time, 0, limit)
	statusByDate := make(map[string]marketcalendar.CoverageStatus)
	for scanned := 0; scanned < maxScan && len(periods) < limit; scanned++ {
		dateText := cursor.Format("2006-01-02")
		status, cached := statusByDate[dateText]
		if !cached {
			civil, parseErr := marketcalendar.ParseCivilDate(dateText)
			if parseErr != nil {
				return nil, parseErr
			}
			var statusErr error
			status, statusErr = calendar.Status(civil)
			if statusErr != nil {
				return nil, fmt.Errorf("StockCN calendar does not cover %s: %w", dateText, statusErr)
			}
			statusByDate[dateText] = status
		}
		if status == marketcalendar.TradingDay {
			minuteOfDay := cursor.Hour()*60 + cursor.Minute()
			for _, session := range sessions {
				if minuteOfDay >= session[0] && minuteOfDay+1 <= session[1] && !cursor.Add(time.Minute).Add(collectorCanarySettleDelay).After(now) {
					periods = append(periods, cursor.UTC())
					break
				}
			}
		}
		cursor = cursor.Add(-time.Minute)
	}
	if len(periods) == 0 {
		return nil, errors.New("StockCN calendar has no settled canary period in its bounded lookback")
	}
	eligible := periods[:0]
	for _, period := range periods {
		if now.UTC().Sub(period) <= maxAge {
			eligible = append(eligible, period)
		}
	}
	if len(eligible) == 0 {
		return nil, fmt.Errorf("latest StockCN canary period %s exceeds provider history window for %s after reservation and safety margins", periods[0].Format(time.RFC3339), strings.ToLower(strings.TrimSpace(entry.GetProvider())))
	}
	return eligible, nil
}

// These bounds mirror the currently registered StockCN provider contracts.
// Unknown providers fail closed: canary selection must never assume an
// arbitrary historical range when the provider only serves a recent page.
func stockCNCanaryProviderHistory(provider string) (time.Duration, error) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "tdx":
		return 365 * 24 * time.Hour, nil
	case "eastmoney", "sina", "tencent":
		return 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("StockCN canary provider %q does not declare a supported history window", provider)
	}
}

func (p *collectorSCFCanaryProof) expectation() *storagepb.DatasetPeriodExpectation {
	if p == nil || p.entry == nil {
		return nil
	}
	return &storagepb.DatasetPeriodExpectation{
		SpaceId: p.entry.GetSpaceId(), DatasetId: p.entry.GetDatasetId(), Frequency: p.entry.GetFrequency(),
		PeriodTime: p.period.UTC().Unix(), SeriesHash: p.entry.GetSeriesHash(), ExpectedCount: p.entry.GetExpectedCount(), DeadlineAt: p.deadlineAt,
		ReservationId:  p.reservationID,
		SeriesSnapshot: []*storagepb.DatasetPeriodSeries{{SeriesIndex: p.entry.GetSeriesIndex(), SubjectId: p.entry.GetSubjectId(), SeriesTag: p.entry.GetSeriesTag()}},
	}
}

func (p *collectorSCFCanaryProof) ensurePeriod(ctx context.Context) error {
	if p == nil || p.entry == nil || p.access.primary == nil || p.access.periodAuth == nil {
		return errors.New("Primary period initializer is unavailable")
	}
	if p.deadlineAt <= 0 {
		p.deadlineAt = p.currentTime().Add(collectorCanaryReservationTTL).Unix()
	}
	expectation := p.expectation()
	rsp, err := p.access.primary.EnsureDatasetPeriod(ctx, &storagepb.PrimaryEnsureDatasetPeriodReq{
		AuthInfo: p.access.periodAuth, Expectation: expectation,
	})
	if err != nil {
		return fmt.Errorf("RPC: %w", err)
	}
	if rsp == nil || rsp.GetRetInfo() == nil {
		return errors.New("Primary returned no successful period response")
	}
	if rsp.GetRetInfo().GetCode() == storagepb.ErrorCode_CONFLICT {
		return errCollectorCanaryPeriodOccupied
	}
	if rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return fmt.Errorf("Primary rejected period initialization: %s", rsp.GetRetInfo().GetMsg())
	}
	if !strings.EqualFold(strings.TrimSpace(rsp.GetStatus()), "waiting") {
		return fmt.Errorf("%w: status=%s", errCollectorCanaryPeriodOccupied, strings.TrimSpace(rsp.GetStatus()))
	}
	if rsp.GetDeadlineAt() <= p.currentTime().Add(collectorCanaryProofTimeout).Unix() {
		return fmt.Errorf("canary period deadline leaves less than %s for the business proof", collectorCanaryProofTimeout)
	}
	p.deadlineAt = rsp.GetDeadlineAt()
	return nil
}

func (p *collectorSCFCanaryProof) periodIsUnused(ctx context.Context) (bool, error) {
	status, err := p.readPeriodStatus(ctx)
	if err != nil {
		return false, err
	}
	if status != "not_found" {
		return false, nil
	}
	primary, err := p.readPrimaryRow(ctx)
	if err != nil {
		return false, err
	}
	view, complete, err := p.readViewRow(ctx)
	if err != nil {
		return false, err
	}
	return !primary && !view && complete, nil
}

func (p *collectorSCFCanaryProof) readPeriodStatus(ctx context.Context) (string, error) {
	rsp, err := p.access.primary.GetDatasetPeriodStatus(ctx, &storagepb.PrimaryGetDatasetPeriodStatusReq{
		AuthInfo: p.access.periodAuth, Expectation: p.expectation(),
	})
	if err != nil {
		return "", fmt.Errorf("read Primary period status: %w", err)
	}
	if rsp == nil || rsp.GetRetInfo() == nil {
		return "", errors.New("Primary period status returned no ret_info")
	}
	if rsp.GetRetInfo().GetCode() == storagepb.ErrorCode_NOT_FOUND {
		return "not_found", nil
	}
	if rsp.GetRetInfo().GetCode() == storagepb.ErrorCode_CONFLICT {
		return "occupied", nil
	}
	if rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return "", fmt.Errorf("Primary period status failed: %s", rsp.GetRetInfo().GetMsg())
	}
	if rsp.GetSeriesHash() != p.entry.GetSeriesHash() || rsp.GetExpectedCount() != p.entry.GetExpectedCount() {
		return "occupied", nil
	}
	return strings.ToLower(strings.TrimSpace(rsp.GetStatus())), nil
}

func (p *collectorSCFCanaryProof) readPrimaryRow(ctx context.Context) (bool, error) {
	key := p.timeSeriesKey()
	rsp, err := p.access.primary.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{
		AuthInfo: p.access.primaryAuth, SpaceId: p.entry.GetSpaceId(), DatasetId: p.entry.GetDatasetId(),
		Keys: []*storagepb.TimeSeriesKey{key}, ColumnNames: collectorCanaryReadFields(p.entry.GetOutputFields()),
	})
	if err != nil {
		return false, fmt.Errorf("read exact Primary row: %w", err)
	}
	if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return false, errors.New("exact Primary row read returned no successful response")
	}
	rowPresent, err := collectorCanaryExactRows(rsp.GetRows(), key, collectorCanaryReadFields(p.entry.GetOutputFields()))
	if err != nil {
		return false, fmt.Errorf("exact Primary row identity: %w", err)
	}
	return rowPresent, nil
}

func (p *collectorSCFCanaryProof) readViewRow(ctx context.Context) (bool, bool, error) {
	tag := p.entry.GetSeriesTag()
	key := p.timeSeriesKey()
	rsp, err := p.access.view.QueryTimeSeriesRows(ctx, &storagepb.QueryTimeSeriesRowsReq{
		AuthInfo: p.access.viewAuth, SpaceId: p.entry.GetSpaceId(), ViewId: p.entry.GetViewId(),
		Selectors:   []*storagepb.TimeSeriesSelector{{SpaceId: key.GetSpaceId(), DatasetId: key.GetDatasetId(), SubjectId: key.GetSubjectId(), Freq: key.GetFreq(), SeriesTag: &tag}},
		TimeRange:   &storagepb.TimeRange{StartTime: p.period.UTC().Format(time.RFC3339Nano), EndTime: p.period.Add(p.interval).UTC().Format(time.RFC3339Nano)},
		ColumnNames: collectorCanaryReadFields(p.entry.GetOutputFields()), Limit: 1, TotalMode: commonpb.TotalMode_NONE,
	})
	if err != nil {
		return false, false, fmt.Errorf("query exact task-owned View row: %w", err)
	}
	if rsp == nil || rsp.GetRetInfo() == nil {
		return false, false, errors.New("task-owned View query returned no ret_info")
	}
	if rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS && rsp.GetRetInfo().GetCode() != storagepb.ErrorCode_VIEW_NOT_READY {
		return false, false, fmt.Errorf("task-owned View query failed: %s", rsp.GetRetInfo().GetMsg())
	}
	if rsp.GetRetInfo().GetCode() == storagepb.ErrorCode_VIEW_NOT_READY {
		return false, false, nil
	}
	rowPresent, err := collectorCanaryExactRows(rsp.GetRows(), key, collectorCanaryReadFields(p.entry.GetOutputFields()))
	if err != nil {
		return false, false, fmt.Errorf("exact task-owned View row identity: %w", err)
	}
	return rowPresent, rsp.GetComplete(), nil
}

func (p *collectorSCFCanaryProof) timeSeriesKey() *storagepb.TimeSeriesKey {
	return &storagepb.TimeSeriesKey{
		SpaceId: p.entry.GetSpaceId(), DatasetId: p.entry.GetDatasetId(), SubjectId: p.entry.GetSubjectId(),
		Freq: p.entry.GetFrequency(), DataTime: p.period.UTC().Format(time.RFC3339Nano), SeriesTag: p.entry.GetSeriesTag(),
	}
}

func collectorCanaryExactRows(rows []*storagepb.TimeSeriesRow, expected *storagepb.TimeSeriesKey, expectedFields []string) (bool, error) {
	if len(rows) == 0 {
		return false, nil
	}
	if len(rows) != 1 || rows[0] == nil {
		return false, fmt.Errorf("exact Storage read returned %d rows; expected at most one", len(rows))
	}
	key := rows[0].GetKey()
	if key == nil {
		return false, errors.New("exact Storage read returned a row without a key")
	}
	dataTime, err := time.Parse(time.RFC3339Nano, key.GetDataTime())
	if err != nil {
		return false, fmt.Errorf("exact Storage row has invalid data_time %q: %w", key.GetDataTime(), err)
	}
	expectedTime, err := time.Parse(time.RFC3339Nano, expected.GetDataTime())
	if err != nil {
		return false, fmt.Errorf("canary target has invalid data_time %q: %w", expected.GetDataTime(), err)
	}
	if key.GetSpaceId() != expected.GetSpaceId() || key.GetDatasetId() != expected.GetDatasetId() ||
		key.GetSubjectId() != expected.GetSubjectId() || key.GetFreq() != expected.GetFreq() || key.GetSeriesTag() != expected.GetSeriesTag() ||
		!dataTime.UTC().Equal(expectedTime.UTC()) {
		return false, fmt.Errorf("exact Storage row identity does not match target period %s/%s/%s/%s/%s/%s",
			expected.GetSpaceId(), expected.GetDatasetId(), expected.GetSubjectId(), expected.GetFreq(), expected.GetSeriesTag(), expected.GetDataTime())
	}
	fields := make(map[string]struct{}, len(rows[0].GetFields()))
	for _, field := range rows[0].GetFields() {
		if field != nil && field.GetValue() != nil {
			fields[field.GetFieldId()] = struct{}{}
		}
	}
	for _, field := range expectedFields {
		if _, ok := fields[field]; !ok {
			return false, fmt.Errorf("exact Storage row for target period is missing requested field %q", field)
		}
	}
	return true, nil
}

func collectorCanaryReadFields(fields []string) []string {
	if len(fields) == 0 {
		return []string{"close"}
	}
	return append([]string(nil), fields...)
}

func (p *collectorSCFCanaryProof) verify(ctx context.Context) error {
	if p == nil || p.entry == nil || p.period.IsZero() || p.interval <= 0 {
		return errors.New("SCF canary verification incomplete: target period binding is missing")
	}
	deadline := p.currentTime().Add(collectorCanaryProofTimeout)
	if parentDeadline, ok := ctx.Deadline(); ok && parentDeadline.Before(deadline) {
		deadline = parentDeadline
	}
	var lastPrimary, lastView, lastViewComplete bool
	lastStatus := "not_found"
	for {
		status, err := p.readPeriodStatus(ctx)
		if err != nil {
			return err
		}
		if status != "not_found" && status != "waiting" && status != "complete" {
			return fmt.Errorf("SCF canary period ended in unacceptable state %q (only complete is publishable)", status)
		}
		lastStatus = status
		if status == "complete" {
			primary, primaryErr := p.readPrimaryRow(ctx)
			if primaryErr != nil {
				return primaryErr
			}
			view, viewComplete, viewErr := p.readViewRow(ctx)
			if viewErr != nil {
				return viewErr
			}
			lastPrimary, lastView, lastViewComplete = primary, view, viewComplete
			if primary && view && viewComplete {
				return nil
			}
		}
		if !p.currentTime().Before(deadline) {
			return fmt.Errorf("SCF canary verification incomplete: task=%s period=%s status=%s primary_row=%t view_row=%t view_complete=%t; publish requires status=complete and the exact Primary and task-owned View rows", p.entry.GetTaskId(), p.period.Format(time.RFC3339), lastStatus, lastPrimary, lastView, lastViewComplete)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for SCF canary Storage proof: %w", ctx.Err())
		case <-time.After(collectorCanaryProofPollInterval):
		}
	}
}
