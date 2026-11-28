// Package service implements the ArchiveMgr console API and the ArchiveWorker internal API.
package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/config"
	"github.com/mooyang-code/moox/modules/archive/internal/store"
	"github.com/mooyang-code/moox/packages/barclock"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/codec"
)

// Dataset 是创建与执行任务所需的数据集信息，来自 Storage 的 Metadata.GetDataset / ListDatasets。
type Dataset struct {
	SpaceID   string
	DatasetID string
	Name      string
	DataKind  string
	Freq      string
	Status    string
	// Retention 是线上保留期："<n>h" 或 "forever"。
	Retention string
	// 以下是写进归档根目录元数据文件的数据集信息。
	Description     string
	DataSourceID    string
	RetentionSource string
	SubjectTags     []string
	Attributes      map[string]string
	// CreatedAt、UpdatedAt 是 Storage 元数据的创建、更新时刻（Unix 毫秒）。
	CreatedAt int64
	UpdatedAt int64
	Revision  uint64
}

// DatasetProvider 读取数据集与空间信息；数据集或空间不存在时分别返回 ErrDatasetNotFound、ErrSpaceNotFound。
type DatasetProvider interface {
	GetDataset(ctx context.Context, spaceID, datasetID string) (Dataset, error)
	ListDatasets(ctx context.Context) ([]Dataset, error)
	GetSpaceTimezone(ctx context.Context, spaceID string) (string, error)
}

// maxListedTasks 是一次列出全部任务时的上限（单用户系统的任务数远小于它）。
const maxListedTasks = 100000

// metadataCacheTTL 是空间时区与数据集元数据（线上保留期、名称等）内存缓存的有效期；
// 两者统一按同一策略缓存，Storage 抖动时沿用旧值，明确返回不存在时失效。
const metadataCacheTTL = 5 * time.Minute

var (
	// ErrDatasetNotFound 表示数据集在 Storage 中不存在。
	ErrDatasetNotFound = errors.New("dataset not found")
	// ErrSpaceNotFound 表示空间在 Storage 中不存在。
	ErrSpaceNotFound = errors.New("space not found")
)

type spaceTZEntry struct {
	tz        string
	fetchedAt time.Time
}

type datasetCacheEntry struct {
	ds        Dataset
	fetchedAt time.Time
}

// Service 持有 ArchiveMgr 与 ArchiveWorker 共用的状态。
type Service struct {
	store    *store.Store
	cfg      *config.Config
	datasets DatasetProvider
	now      func() time.Time

	mu                    sync.Mutex
	lastRefresh           time.Time
	lastCleanup           time.Time
	lastVacuum            time.Time
	spaceTZ               map[string]spaceTZEntry
	datasetCache          map[string]datasetCacheEntry
	ensureIssues          []string
	listDatasetsFailCount int
}

// Option 调整 Service。
type Option func(*Service)

// WithClock 替换时钟，测试使用。
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// New 构造服务。
func New(db *store.Store, cfg *config.Config, datasets DatasetProvider, opts ...Option) (*Service, error) {
	if db == nil || cfg == nil || datasets == nil {
		return nil, errors.New("archive service requires a store, config and dataset provider")
	}
	s := &Service{store: db, cfg: cfg, datasets: datasets, now: func() time.Time { return time.Now().UTC() }}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// Mgr 返回 ArchiveMgr 的实现。
func (s *Service) Mgr() *MgrService { return &MgrService{svc: s} }

// Worker 返回 ArchiveWorker 的实现。
func (s *Service) Worker() *WorkerService { return &WorkerService{svc: s} }

func (s *Service) clock() time.Time { return s.now().UTC() }

func success() *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_SUCCESS, Msg: "success"}
}

func invalid(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_INVALID_PARAM, Msg: err.Error()}
}

func inner(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_INNER_ERR, Msg: err.Error()}
}

func conflict(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_CONFLICT, Msg: err.Error()}
}

func notFound(err error) *commonpb.RetInfo {
	return &commonpb.RetInfo{Code: commonpb.ErrorCode_NOT_FOUND, Msg: err.Error()}
}

// fail 把 store 错误映射成返回码。
func fail(err error) *commonpb.RetInfo {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, ErrDatasetNotFound), errors.Is(err, ErrSpaceNotFound):
		return notFound(err)
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrHasPartitions):
		return conflict(err)
	default:
		return inner(err)
	}
}

// spaceTimezone 从 Storage 的空间元数据读取空间时区并按 metadataCacheTTL 缓存；
// Storage 临时不可用且有缓存时沿用旧值，避免抖动中断任务下发与总览。
func (s *Service) spaceTimezone(ctx context.Context, spaceID string) (string, error) {
	spaceID = strings.TrimSpace(spaceID)
	if spaceID == "" {
		return "UTC", nil
	}
	now := s.clock()
	s.mu.Lock()
	entry, hasCached := s.spaceTZ[spaceID]
	s.mu.Unlock()
	if hasCached && now.Sub(entry.fetchedAt) < metadataCacheTTL {
		return entry.tz, nil
	}
	tz, err := s.datasets.GetSpaceTimezone(ctx, spaceID)
	if err != nil {
		if errors.Is(err, ErrSpaceNotFound) {
			if hasCached {
				s.mu.Lock()
				delete(s.spaceTZ, spaceID)
				s.mu.Unlock()
			}
			return "", err
		}
		if hasCached {
			return entry.tz, nil
		}
		return "", err
	}
	tz = strings.TrimSpace(tz)
	if tz == "" {
		tz = "UTC"
	}
	if _, err := time.LoadLocation(tz); err != nil {
		return "", fmt.Errorf("空间 %s 的时区 %q 无效: %w", spaceID, tz, err)
	}
	s.mu.Lock()
	if s.spaceTZ == nil {
		s.spaceTZ = map[string]spaceTZEntry{}
	}
	s.spaceTZ[spaceID] = spaceTZEntry{tz: tz, fetchedAt: now}
	s.mu.Unlock()
	return tz, nil
}

// datasetInfo 从 Storage 读取数据集元数据（保留期、名称等）并按 metadataCacheTTL 内存缓存（与 spaceTimezone 同一策略）；
// Storage 明确返回不存在时清除缓存并返回 ErrDatasetNotFound，临时故障且有缓存时沿用旧值。
func (s *Service) datasetInfo(ctx context.Context, spaceID, datasetID string) (Dataset, error) {
	spaceID = strings.TrimSpace(spaceID)
	datasetID = strings.TrimSpace(datasetID)
	key := spaceID + "/" + datasetID
	now := s.clock()
	s.mu.Lock()
	entry, hasCached := s.datasetCache[key]
	s.mu.Unlock()
	if hasCached && now.Sub(entry.fetchedAt) < metadataCacheTTL {
		return entry.ds, nil
	}
	ds, err := s.datasets.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		if errors.Is(err, ErrDatasetNotFound) {
			if hasCached {
				s.evictDataset(spaceID, datasetID)
			}
			return Dataset{}, err
		}
		if hasCached {
			return entry.ds, nil
		}
		return Dataset{}, err
	}
	ds.SpaceID = spaceID
	ds.DatasetID = datasetID
	s.cacheDataset(ds, now)
	return ds, nil
}

func (s *Service) cacheDataset(ds Dataset, now time.Time) {
	spaceID := strings.TrimSpace(ds.SpaceID)
	datasetID := strings.TrimSpace(ds.DatasetID)
	if spaceID == "" || datasetID == "" {
		return
	}
	ds.SpaceID = spaceID
	ds.DatasetID = datasetID
	s.mu.Lock()
	if s.datasetCache == nil {
		s.datasetCache = map[string]datasetCacheEntry{}
	}
	s.datasetCache[spaceID+"/"+datasetID] = datasetCacheEntry{ds: ds, fetchedAt: now}
	s.mu.Unlock()
}

func (s *Service) evictDataset(spaceID, datasetID string) {
	key := strings.TrimSpace(spaceID) + "/" + strings.TrimSpace(datasetID)
	s.mu.Lock()
	delete(s.datasetCache, key)
	s.mu.Unlock()
}

const spaceIDMetadata = "X-Space-Id"

// callerSpace 返回调用方（经 Console 携带）声明的空间。没有空间元数据的调用（CLI、内部服务）不受空间限制；
// 多个不同的空间值视为非法。
func callerSpace(ctx context.Context) (space string, scoped bool, err error) {
	for key, value := range codec.Message(ctx).ServerMetaData() {
		if !strings.EqualFold(key, spaceIDMetadata) {
			continue
		}
		candidate := strings.TrimSpace(string(value))
		if scoped && candidate != space {
			return "", true, fmt.Errorf("请求头里的空间 ID 相互冲突")
		}
		space, scoped = candidate, true
	}
	if scoped && space == "" {
		return "", true, fmt.Errorf("请求头里的空间 ID 为空")
	}
	return space, scoped, nil
}

// resolveSpace 合并请求里的 space_id 与请求头里的空间：两者都有时必须一致；都没有时 required 为真则报错。
func resolveSpace(ctx context.Context, requested string, required bool) (string, error) {
	requested = strings.TrimSpace(requested)
	header, scoped, err := callerSpace(ctx)
	if err != nil {
		return "", err
	}
	if scoped && requested != "" && requested != header {
		return "", fmt.Errorf("请求的空间 %q 与调用方所在空间 %q 不一致", requested, header)
	}
	if scoped {
		return header, nil
	}
	if requested == "" && required {
		return "", errors.New("space_id 不能为空")
	}
	return requested, nil
}

// spaceMarket 从共享包 barclock 读取空间的日终时刻（本地 HH:MM，全天交易为空）与是否全天交易；
// 未登记的空间按全天交易处理。
func spaceMarket(spaceID string) (dayClose string, allDay bool) {
	market, err := barclock.MarketFor(spaceID)
	if err != nil {
		return "", true
	}
	return market.DayClose, market.Alignment != barclock.AlignmentSession
}

// parseRetention 解析线上保留期："<n>h" 或 "forever"（不限）；空串说明没有读到数据集信息，按错误处理。
func parseRetention(value string) (limit time.Duration, forever bool, err error) {
	value = strings.TrimSpace(value)
	if value == "forever" {
		return 0, true, nil
	}
	d, err := time.ParseDuration(value)
	if err != nil || d <= 0 {
		return 0, false, fmt.Errorf("线上保留期 %q 无效", value)
	}
	return d, false, nil
}
