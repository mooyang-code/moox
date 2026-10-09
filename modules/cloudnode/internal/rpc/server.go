package rpc

import (
	"context"
	"errors"
	"sync"

	"github.com/mooyang-code/moox/modules/cloudnode/internal/cloudcredential"
	tencentscf "github.com/mooyang-code/moox/modules/cloudnode/internal/providers/tencentscf"
	"github.com/mooyang-code/moox/modules/cloudnode/internal/store"
	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/report"
	"gorm.io/gorm"
)

// Service is the independent cloudnode service implementation.
type Service struct {
	pb.UnimplementedCloudNodeMgr
	catalog            *store.CatalogRepository
	credentialResolver interface {
		Resolve(context.Context, store.CloudAccount) (cloudcredential.TencentCredential, error)
	}
	scfClientFactory      func(cloudcredential.TencentCredential) scfProvisioner
	executeNodeBatchItem  func(context.Context, store.NodeBatchItem) (string, error)
	nodeBatchTakenHook    func([]store.NodeBatchItem)
	moduleMetrics         *report.ModuleMetrics
	publishLeaseValidator CollectorPublishLeaseValidator
	packageObjectsFactory func(store.CloudAccount, cloudcredential.TencentCredential) packageObjectStore
	nodeBatchWorkers      sync.WaitGroup
}

type scfProvisioner interface {
	EnsureNamespace(context.Context, string, string) error
	GetFunction(context.Context, tencentscf.FunctionRef) (*tencentscf.FunctionInfo, error)
	CreateFunction(context.Context, tencentscf.CreateFunctionRequest) (*tencentscf.CreateFunctionResponse, error)
	DeleteFunction(context.Context, tencentscf.FunctionRef) error
	UpdateFunctionCode(context.Context, tencentscf.UpdateFunctionCodeRequest) (*tencentscf.UpdateFunctionCodeResponse, error)
	UpdateFunctionConfiguration(context.Context, tencentscf.UpdateFunctionConfigurationRequest) (*tencentscf.UpdateFunctionConfigurationResponse, error)
	EnsureTimerTrigger(context.Context, tencentscf.TimerTriggerRequest) (*tencentscf.TimerTriggerInfo, error)
	GetTimerTrigger(context.Context, tencentscf.FunctionRef, string) (*tencentscf.TimerTriggerInfo, error)
	DeleteTimerTrigger(context.Context, tencentscf.TimerTriggerRequest) error
	InvokeFunction(context.Context, tencentscf.InvokeFunctionRequest) (*tencentscf.InvokeFunctionResponse, error)
}

type Option func(*Service)

func WithModuleMetrics(metrics *report.ModuleMetrics) Option {
	return func(s *Service) { s.moduleMetrics = metrics }
}

func WithCollectorPublishLeaseValidator(validator CollectorPublishLeaseValidator) Option {
	return func(s *Service) { s.publishLeaseValidator = validator }
}

func WithCredentialResolver(resolver interface {
	Resolve(context.Context, store.CloudAccount) (cloudcredential.TencentCredential, error)
}) Option {
	return func(s *Service) { s.credentialResolver = resolver }
}

// New creates a cloudnode RPC service.
func New(dbm *store.Store, opts ...Option) *Service {
	svc := &Service{
		catalog:               dbm.Catalog(),
		scfClientFactory:      defaultSCFClientFactory,
		packageObjectsFactory: defaultPackageObjectsFactory,
	}
	for _, opt := range opts {
		opt(svc)
	}
	return svc
}

func defaultSCFClientFactory(credential cloudcredential.TencentCredential) scfProvisioner {
	return tencentscf.New(credential.SecretID, credential.SecretKey)
}

func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "ok"}
}

func retErr(code pb.ErrorCode, msg string) *pb.RetInfo {
	return &pb.RetInfo{Code: code, Msg: msg}
}

func retFromError(err error) *pb.RetInfo {
	switch {
	case errors.Is(err, store.ErrPollingNodeNotFound):
		return retErr(pb.ErrorCode_NOT_FOUND, "cloud node not found")
	case errors.Is(err, gorm.ErrRecordNotFound):
		return retErr(pb.ErrorCode_NOT_FOUND, "resource not found")
	default:
		return retErr(pb.ErrorCode_INNER_ERR, "internal error")
	}
}
