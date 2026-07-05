package rpc

import (
	"context"
	"errors"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/config"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/jobqueue"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/projection"
	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencent-scf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/repository"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/storage"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"gorm.io/gorm"
)

// Service is the independent cloudnode service implementation.
type Service struct {
	pb.UnimplementedCloudNodeMgr
	dbm              *storage.Manager
	jobItemRepo      repository.QueueStore
	executionQueue   jobqueue.ExecutionQueue
	projectionRepo   *projection.Repository
	projector        *projection.Projector
	heartbeatSink    projection.HeartbeatSink
	catalog          *repository.CatalogRepository
	scfClientFactory func(repository.CloudAccount) scfProvisioner
}

type scfProvisioner interface {
	GetFunction(context.Context, tencentscf.FunctionRef) (*tencentscf.FunctionInfo, error)
	CreateFunction(context.Context, tencentscf.CreateFunctionRequest) (*tencentscf.CreateFunctionResponse, error)
	UpdateFunctionCode(context.Context, tencentscf.UpdateFunctionCodeRequest) (*tencentscf.UpdateFunctionCodeResponse, error)
}

type Option func(*Service)

func WithExecutionQueue(queue jobqueue.ExecutionQueue) Option {
	return func(s *Service) { s.executionQueue = queue }
}

func WithProjectionRepository(repo *projection.Repository) Option {
	return func(s *Service) { s.projectionRepo = repo }
}

func WithProjector(projector *projection.Projector) Option {
	return func(s *Service) { s.projector = projector }
}

func WithHeartbeatSink(sink projection.HeartbeatSink) Option {
	return func(s *Service) { s.heartbeatSink = sink }
}

// New creates a cloudnode RPC service.
func New(dbm *storage.Manager, opts ...Option) *Service {
	cfg := config.Global()
	svc := &Service{
		dbm:              dbm,
		jobItemRepo:      newConfiguredJobItemRepository(dbm.DB(), cfg.JobItem),
		projectionRepo:   projection.NewRepository(dbm.DB(), projection.RepositoryOptions{RecoverAfterMillis: cfg.JobItem.RecoverAfterMillis, DefaultMaxAttempts: cfg.JobItem.DefaultMaxAttempts}),
		catalog:          repository.NewCatalogRepository(dbm.DB()),
		scfClientFactory: defaultSCFClientFactory,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

func defaultSCFClientFactory(account repository.CloudAccount) scfProvisioner {
	return tencentscf.New(account.SecretID, account.SecretKey)
}

func newConfiguredJobItemRepository(db *gorm.DB, cfg config.JobItemConfig) repository.QueueStore {
	return repository.NewJobItemRepositoryWithOptions(db, repository.JobItemRepositoryOptions{
		DefaultLimit:       cfg.DefaultLimit,
		MaxLimit:           cfg.MaxLimit,
		RecoverAfterMillis: cfg.RecoverAfterMillis,
		DefaultMaxAttempts: cfg.DefaultMaxAttempts,
	})
}

func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "ok"}
}

func retErr(code pb.ErrorCode, msg string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: msg}
}

func retFromError(err error) *pb.RetInfo {
	switch {
	case errors.Is(err, repository.ErrPollingNodeNotFound):
		return retErr(pb.ErrorCode_NOT_FOUND, "cloud node not found")
	case errors.Is(err, repository.ErrStaleJobItemAttempt):
		return retErr(pb.ErrorCode_INVALID_PARAM, "conflict: job item attempt is stale")
	case errors.Is(err, repository.ErrJobItemInactive):
		return retErr(pb.ErrorCode_INVALID_PARAM, "conflict: job item is not running")
	case errors.Is(err, repository.ErrJobItemConflict):
		return retErr(pb.ErrorCode_INVALID_PARAM, "conflict: job item state does not allow this operation")
	case errors.Is(err, projection.ErrConflict):
		return retErr(pb.ErrorCode_INVALID_PARAM, "conflict: job item state does not allow this operation")
	case errors.Is(err, projection.ErrStaleAttempt):
		return retErr(pb.ErrorCode_INVALID_PARAM, "conflict: job item attempt is stale")
	case errors.Is(err, projection.ErrInactive):
		return retErr(pb.ErrorCode_INVALID_PARAM, "conflict: job item is not running")
	case errors.Is(err, gorm.ErrRecordNotFound):
		return retErr(pb.ErrorCode_NOT_FOUND, "resource not found")
	default:
		return retErr(pb.ErrorCode_INNER_ERR, "internal error")
	}
}
