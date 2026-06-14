package control

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	pb "github.com/mooyang-code/moox/modules/control/proto/controlgen"
	"github.com/rs/xid"
)

type Service struct {
	mu             sync.RWMutex
	workspaces     map[string]*pb.WorkspaceSummary
	datasets       map[string]*pb.DataSetConfig
	fields         map[string][]*pb.FieldConfig
	routes         map[string]*pb.StorageRouteConfig
	bindings       map[string]*pb.CollectorBindingConfig
	changes        map[string]*pb.MetadataChange
	collectors     map[string]*pb.CollectorInfo
	collectorTasks map[string][]*pb.CollectorTask
	nodes          map[string]*pb.NodeInfo
	tasks          map[string]*pb.TaskInfo
}

var (
	_ pb.ControlServiceService   = (*Service)(nil)
	_ pb.CollectorServiceService = (*Service)(nil)
	_ pb.NodeServiceService      = (*Service)(nil)
	_ pb.TaskServiceService      = (*Service)(nil)
)

func NewService() *Service {
	return &Service{
		workspaces:     make(map[string]*pb.WorkspaceSummary),
		datasets:       make(map[string]*pb.DataSetConfig),
		fields:         make(map[string][]*pb.FieldConfig),
		routes:         make(map[string]*pb.StorageRouteConfig),
		bindings:       make(map[string]*pb.CollectorBindingConfig),
		changes:        make(map[string]*pb.MetadataChange),
		collectors:     make(map[string]*pb.CollectorInfo),
		collectorTasks: make(map[string][]*pb.CollectorTask),
		nodes:          make(map[string]*pb.NodeInfo),
		tasks:          make(map[string]*pb.TaskInfo),
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

func (s *Service) RegisterCollector(_ context.Context, req *pb.RegisterCollectorReq) (*pb.RegisterCollectorRsp, error) {
	collector := req.GetCollector()
	if collector == nil || collector.GetName() == "" {
		return &pb.RegisterCollectorRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, errors.New("collector.name is required"))}, nil
	}
	if collector.CollectorId == "" {
		collector.CollectorId = defaultID(collector.GetName(), "collector")
	}
	s.mu.Lock()
	s.collectors[collector.GetCollectorId()] = collector
	s.mu.Unlock()
	return &pb.RegisterCollectorRsp{RetInfo: retOK(), Collector: collector}, nil
}

func (s *Service) Heartbeat(_ context.Context, req *pb.HeartbeatReq) (*pb.HeartbeatRsp, error) {
	if req.GetCollectorId() == "" {
		return &pb.HeartbeatRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, errors.New("collector_id is required"))}, nil
	}
	s.mu.Lock()
	if collector := s.collectors[req.GetCollectorId()]; collector != nil {
		collector.Status = req.GetStatus()
	}
	s.mu.Unlock()
	return &pb.HeartbeatRsp{RetInfo: retOK()}, nil
}

func (s *Service) AssignTask(_ context.Context, req *pb.AssignTaskReq) (*pb.AssignTaskRsp, error) {
	s.mu.RLock()
	tasks := append([]*pb.CollectorTask(nil), s.collectorTasks[req.GetCollectorId()]...)
	s.mu.RUnlock()
	return &pb.AssignTaskRsp{RetInfo: retOK(), Tasks: tasks}, nil
}

func (s *Service) ReportTaskStatus(_ context.Context, req *pb.ReportTaskStatusReq) (*pb.ReportTaskStatusRsp, error) {
	if req.GetTaskId() == "" {
		return &pb.ReportTaskStatusRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, errors.New("task_id is required"))}, nil
	}
	s.mu.Lock()
	if task := s.tasks[req.GetTaskId()]; task != nil {
		task.Status = req.GetStatus()
		task.Message = req.GetMessage()
	}
	s.mu.Unlock()
	return &pb.ReportTaskStatusRsp{RetInfo: retOK()}, nil
}

func (s *Service) RegisterNode(_ context.Context, req *pb.RegisterNodeReq) (*pb.RegisterNodeRsp, error) {
	node := req.GetNode()
	if node == nil || node.GetName() == "" {
		return &pb.RegisterNodeRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, errors.New("node.name is required"))}, nil
	}
	if node.NodeId == "" {
		node.NodeId = defaultID(node.GetName(), "node")
	}
	s.mu.Lock()
	s.nodes[node.GetNodeId()] = node
	s.mu.Unlock()
	return &pb.RegisterNodeRsp{RetInfo: retOK(), Node: node}, nil
}

func (s *Service) UpdateNodeStatus(_ context.Context, req *pb.UpdateNodeStatusReq) (*pb.UpdateNodeStatusRsp, error) {
	s.mu.Lock()
	node := s.nodes[req.GetNodeId()]
	if node != nil {
		node.Status = req.GetStatus()
	}
	s.mu.Unlock()
	if node == nil {
		return &pb.UpdateNodeStatusRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, errors.New("node not found"))}, nil
	}
	return &pb.UpdateNodeStatusRsp{RetInfo: retOK(), Node: node}, nil
}

func (s *Service) ListNodes(_ context.Context, req *pb.ListNodesReq) (*pb.ListNodesRsp, error) {
	s.mu.RLock()
	var nodes []*pb.NodeInfo
	for _, node := range s.nodes {
		if req.GetNodeType() != "" && node.GetNodeType() != req.GetNodeType() {
			continue
		}
		if req.GetStatus() != "" && node.GetStatus() != req.GetStatus() {
			continue
		}
		nodes = append(nodes, node)
	}
	s.mu.RUnlock()
	sort.Slice(nodes, func(i, j int) bool { return nodes[i].GetNodeId() < nodes[j].GetNodeId() })
	paged, page := pageSlice(nodes, req.GetPage())
	return &pb.ListNodesRsp{RetInfo: retOK(), Nodes: paged, PageResult: page}, nil
}

func (s *Service) CreateTask(_ context.Context, req *pb.CreateTaskReq) (*pb.CreateTaskRsp, error) {
	task := req.GetTask()
	if task == nil || task.GetTaskType() == "" {
		return &pb.CreateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, errors.New("task.task_type is required"))}, nil
	}
	if task.TaskId == "" {
		task.TaskId = defaultID(task.GetTaskType(), "task")
	}
	if task.Status == pb.TaskStatus_TASK_STATUS_UNSPECIFIED {
		task.Status = pb.TaskStatus_TASK_STATUS_PENDING
	}
	s.mu.Lock()
	s.tasks[task.GetTaskId()] = task
	s.mu.Unlock()
	return &pb.CreateTaskRsp{RetInfo: retOK(), Task: task}, nil
}

func (s *Service) UpdateTask(_ context.Context, req *pb.UpdateTaskReq) (*pb.UpdateTaskRsp, error) {
	task := req.GetTask()
	if task == nil || task.GetTaskId() == "" {
		return &pb.UpdateTaskRsp{RetInfo: retErr(pb.ErrorCode_INVALID_PARAM, errors.New("task_id is required"))}, nil
	}
	s.mu.Lock()
	s.tasks[task.GetTaskId()] = task
	s.mu.Unlock()
	return &pb.UpdateTaskRsp{RetInfo: retOK(), Task: task}, nil
}

func (s *Service) GetTask(_ context.Context, req *pb.GetTaskReq) (*pb.GetTaskRsp, error) {
	s.mu.RLock()
	task := s.tasks[req.GetTaskId()]
	s.mu.RUnlock()
	if task == nil {
		return &pb.GetTaskRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, errors.New("task not found"))}, nil
	}
	return &pb.GetTaskRsp{RetInfo: retOK(), Task: task}, nil
}

func (s *Service) ListTasks(_ context.Context, req *pb.ListTasksReq) (*pb.ListTasksRsp, error) {
	s.mu.RLock()
	var tasks []*pb.TaskInfo
	for _, task := range s.tasks {
		if req.GetWorkspaceId() != "" && task.GetWorkspaceId() != req.GetWorkspaceId() {
			continue
		}
		if req.GetStatus() != pb.TaskStatus_TASK_STATUS_UNSPECIFIED && task.GetStatus() != req.GetStatus() {
			continue
		}
		tasks = append(tasks, task)
	}
	s.mu.RUnlock()
	sort.Slice(tasks, func(i, j int) bool { return tasks[i].GetTaskId() < tasks[j].GetTaskId() })
	paged, page := pageSlice(tasks, req.GetPage())
	return &pb.ListTasksRsp{RetInfo: retOK(), Tasks: paged, PageResult: page}, nil
}

func (s *Service) CancelTask(_ context.Context, req *pb.CancelTaskReq) (*pb.CancelTaskRsp, error) {
	s.mu.Lock()
	task := s.tasks[req.GetTaskId()]
	if task != nil {
		task.Status = pb.TaskStatus_TASK_STATUS_CANCELLED
		task.Message = req.GetReason()
	}
	s.mu.Unlock()
	if task == nil {
		return &pb.CancelTaskRsp{RetInfo: retErr(pb.ErrorCode_NOT_FOUND, errors.New("task not found"))}, nil
	}
	return &pb.CancelTaskRsp{RetInfo: retOK(), Task: task}, nil
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

func pageSlice[T any](items []T, page *pb.Page) ([]T, *pb.PageResult) {
	pageNo := uint32(1)
	size := uint32(1000)
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			size = page.GetSize()
		}
	}
	start := int((pageNo - 1) * size)
	if start > len(items) {
		start = len(items)
	}
	end := start + int(size)
	if end > len(items) {
		end = len(items)
	}
	return items[start:end], &pb.PageResult{Page: pageNo, Size: size, Total: uint64(len(items)), HasMore: end < len(items)}
}
