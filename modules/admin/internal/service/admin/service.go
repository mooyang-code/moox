package admin

import (
	"context"
	"errors"
	"strings"
	"sync"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/rs/xid"
)

// Service 表示 Admin 模块的服务入口。
type Service struct {
	mu             sync.RWMutex
	workspaces     map[string]*pb.WorkspaceSummary
	datasets       map[string]*pb.DataSetConfig
	fields         map[string][]*pb.FieldConfig
	routes         map[string]*pb.StorageRouteConfig
	bindings       map[string]*pb.CollectorBindingConfig
	changes        map[string]*pb.MetadataChange
}

var (
	_ pb.AdminService = (*Service)(nil)
)

func NewService() *Service {
	return &Service{
		workspaces:     make(map[string]*pb.WorkspaceSummary),
		datasets:       make(map[string]*pb.DataSetConfig),
		fields:         make(map[string][]*pb.FieldConfig),
		routes:         make(map[string]*pb.StorageRouteConfig),
		bindings:       make(map[string]*pb.CollectorBindingConfig),
		changes:        make(map[string]*pb.MetadataChange),
	}
}

func (s *Service) CreateWorkspaceWithDefaults(_ context.Context, req *pb.CreateWorkspaceWithDefaultsReq) (*pb.CreateWorkspaceWithDefaultsRsp, error) {
	workspace := req.GetWorkspace()
	if workspace == nil || workspace.GetName() == "" {
		return &pb.CreateWorkspaceWithDefaultsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, errors.New("workspace.name is required"))}, nil
	}
	if workspace.WorkspaceId == "" {
		workspace.WorkspaceId = defaultID(workspace.GetName(), "workspace")
	}
	s.mu.Lock()
	s.workspaces[workspace.GetWorkspaceId()] = workspace
	s.mu.Unlock()
	return &pb.CreateWorkspaceWithDefaultsRsp{RetInfo: retOK(), Workspace: workspace}, nil
}

func (s *Service) ConfigureDataSet(_ context.Context, req *pb.ConfigureDataSetReq) (*pb.ConfigureDataSetRsp, error) {
	dataset := req.GetDataset()
	if dataset == nil || dataset.GetWorkspaceId() == "" || dataset.GetName() == "" {
		return &pb.ConfigureDataSetRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, errors.New("dataset.workspace_id and name are required"))}, nil
	}
	if dataset.DatasetId == "" {
		dataset.DatasetId = defaultID(dataset.GetName(), "dataset")
	}
	s.mu.Lock()
	s.datasets[key(dataset.GetWorkspaceId(), dataset.GetDatasetId())] = dataset
	s.mu.Unlock()
	return &pb.ConfigureDataSetRsp{RetInfo: retOK(), Dataset: dataset}, nil
}

func (s *Service) ConfigureFields(_ context.Context, req *pb.ConfigureFieldsReq) (*pb.ConfigureFieldsRsp, error) {
	if req.GetWorkspaceId() == "" || req.GetDatasetId() == "" {
		return &pb.ConfigureFieldsRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, errors.New("workspace_id and dataset_id are required"))}, nil
	}
	fields := req.GetFields()
	for _, field := range fields {
		if field.FieldId == "" {
			field.FieldId = defaultID(field.GetInterfaceName(), "field")
		}
		if field.DatasetId == "" {
			field.DatasetId = req.GetDatasetId()
		}
	}
	s.mu.Lock()
	s.fields[key(req.GetWorkspaceId(), req.GetDatasetId())] = fields
	s.mu.Unlock()
	return &pb.ConfigureFieldsRsp{RetInfo: retOK(), Fields: fields}, nil
}

func (s *Service) ConfigureStorageRoutes(_ context.Context, req *pb.ConfigureStorageRoutesReq) (*pb.ConfigureStorageRoutesRsp, error) {
	routes := req.GetRoutes()
	for _, route := range routes {
		if route.GetWorkspaceId() == "" || route.GetDatasetId() == "" || route.GetDeviceId() == "" {
			return &pb.ConfigureStorageRoutesRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, errors.New("route workspace_id, dataset_id and device_id are required"))}, nil
		}
		if route.RouteId == "" {
			route.RouteId = defaultID(route.GetDatasetId()+"-"+route.GetDeviceId(), "route")
		}
	}
	s.mu.Lock()
	for _, route := range routes {
		s.routes[route.GetRouteId()] = route
	}
	s.mu.Unlock()
	return &pb.ConfigureStorageRoutesRsp{RetInfo: retOK(), Routes: routes}, nil
}

func (s *Service) ConfigureCollectorBinding(_ context.Context, req *pb.ConfigureCollectorBindingReq) (*pb.ConfigureCollectorBindingRsp, error) {
	binding := req.GetBinding()
	if binding == nil || binding.GetWorkspaceId() == "" || binding.GetDatasetId() == "" || binding.GetDataSource() == "" {
		return &pb.ConfigureCollectorBindingRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, errors.New("binding workspace_id, dataset_id and data_source are required"))}, nil
	}
	if binding.BindingId == "" {
		binding.BindingId = defaultID(binding.GetDatasetId()+"-"+binding.GetDataSource(), "binding")
	}
	s.mu.Lock()
	s.bindings[binding.GetBindingId()] = binding
	s.mu.Unlock()
	return &pb.ConfigureCollectorBindingRsp{RetInfo: retOK(), Binding: binding}, nil
}

func (s *Service) PublishMetadataChange(_ context.Context, req *pb.PublishMetadataChangeReq) (*pb.PublishMetadataChangeRsp, error) {
	change := req.GetChange()
	if change == nil || change.GetWorkspaceId() == "" || change.GetResourceType() == "" {
		return &pb.PublishMetadataChangeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, errors.New("change workspace_id and resource_type are required"))}, nil
	}
	if change.ChangeId == "" {
		change.ChangeId = "change_" + xid.New().String()
	}
	s.mu.Lock()
	s.changes[change.GetChangeId()] = change
	s.mu.Unlock()
	return &pb.PublishMetadataChangeRsp{RetInfo: retOK(), Change: change}, nil
}

func retOK() *pb.RetInfo {
	return &pb.RetInfo{Code: pb.ErrorCode_SUCCESS, Msg: "success"}
}

func retErr(code pb.ErrorCode, err error) *pb.RetInfo {
	if err == nil {
		return &pb.RetInfo{Code: code}
	}
	return &pb.RetInfo{Code: code, Msg: err.Error()}
}

func key(parts ...string) string {
	return strings.Join(parts, "|")
}

func defaultID(name, prefix string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return prefix + "_" + xid.New().String()
	}
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ":", "_")
	return replacer.Replace(name)
}
