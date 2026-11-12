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
	"github.com/mooyang-code/moox/modules/archive/internal/schedule"
	"github.com/mooyang-code/moox/modules/archive/internal/store"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/codec"
)

// Dataset 是创建任务所需的数据集信息，来自 Storage 的 Metadata.GetDataset。
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
	CreatedAt       string
	UpdatedAt       string
	Revision        uint64
}

// DatasetProvider 读取数据集信息；数据集不存在时返回 ErrDatasetNotFound。
type DatasetProvider interface {
	GetDataset(ctx context.Context, spaceID, datasetID string) (Dataset, error)
}

// maxListedTasks 是一次列出全部任务时的上限（单用户系统的任务数远小于它）。
const maxListedTasks = 100000

// ErrDatasetNotFound 表示数据集在 Storage 中不存在。
var ErrDatasetNotFound = errors.New("dataset not found")

// Service 持有 ArchiveMgr 与 ArchiveWorker 共用的状态。
type Service struct {
	store    *store.Store
	cfg      *config.Config
	datasets DatasetProvider
	// collector 读取各空间的采集频率，用来推出避让规则；nil 时一直用默认值。
	collector CollectorFrequencies
	now       func() time.Time
	avoid     avoidanceState

	mu          sync.Mutex
	lastRefresh time.Time
	lastCleanup time.Time
	lastVacuum  time.Time
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

// timing 返回任务算拉取边界用的时序信息（空间时区与本地收盘时刻取自配置，时区无效时按 UTC；避让规则取当前快照）。
func (s *Service) timing(spaceID, freq string, pullEveryBars int) schedule.Timing {
	spaceCfg := s.cfg.Space(spaceID)
	loc, err := time.LoadLocation(spaceCfg.TZ)
	if err != nil {
		loc = time.UTC
	}
	return schedule.Timing{Freq: freq, PullEveryBars: pullEveryBars, Loc: loc, LocalClose: spaceCfg.LocalClose, Avoid: s.avoidance()}
}

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
	case errors.Is(err, store.ErrNotFound), errors.Is(err, ErrDatasetNotFound):
		return notFound(err)
	case errors.Is(err, store.ErrConflict), errors.Is(err, store.ErrHasPartitions):
		return conflict(err)
	default:
		return inner(err)
	}
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
