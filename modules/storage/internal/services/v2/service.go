package v2

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/mooyang-code/moox/modules/storage/pkg/quantstore"
	pb "github.com/mooyang-code/moox/modules/storage/proto/genv2"
	"github.com/rs/xid"
)

type Service struct {
	store *quantstore.Store

	mu                sync.RWMutex
	workspaces        map[string]*pb.Workspace
	exchanges         map[string]*pb.Exchange
	instruments       map[string]*pb.Instrument
	aliases           map[string]*pb.InstrumentAlias
	datasets          map[string]*pb.DataSet
	fields            map[string]*pb.Field
	factorDefs        map[string]*pb.FactorDef
	factorInstances   map[string]*pb.FactorInstance
	dataViews         map[string]*pb.DataView
	storageDevices    map[string]*pb.StorageDevice
	storageRoutes     map[string]*pb.StorageRoute
	collectorBindings map[string]*pb.CollectorDataSetBinding
}

var (
	_ pb.MetadataServiceService = (*Service)(nil)
	_ pb.DataServiceService     = (*Service)(nil)
	_ pb.QueryServiceService    = (*Service)(nil)
	_ pb.AdapterService         = (*Service)(nil)
)

func NewService(root string) *Service {
	return &Service{
		store:             quantstore.New(root),
		workspaces:        make(map[string]*pb.Workspace),
		exchanges:         make(map[string]*pb.Exchange),
		instruments:       make(map[string]*pb.Instrument),
		aliases:           make(map[string]*pb.InstrumentAlias),
		datasets:          make(map[string]*pb.DataSet),
		fields:            make(map[string]*pb.Field),
		factorDefs:        make(map[string]*pb.FactorDef),
		factorInstances:   make(map[string]*pb.FactorInstance),
		dataViews:         make(map[string]*pb.DataView),
		storageDevices:    make(map[string]*pb.StorageDevice),
		storageRoutes:     make(map[string]*pb.StorageRoute),
		collectorBindings: make(map[string]*pb.CollectorDataSetBinding),
	}
}

func (s *Service) CreateWorkspace(_ context.Context, req *pb.CreateWorkspaceReq) (*pb.CreateWorkspaceRsp, error) {
	if req.GetWorkspace() == nil {
		return &pb.CreateWorkspaceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("workspace is required"))}, nil
	}
	workspace := req.GetWorkspace()
	if workspace.WorkspaceId == "" {
		workspace.WorkspaceId = defaultID(workspace.GetName(), "workspace")
	}
	s.mu.Lock()
	s.workspaces[workspace.GetWorkspaceId()] = workspace
	s.mu.Unlock()
	return &pb.CreateWorkspaceRsp{RetInfo: quantstore.Success("success"), Workspace: workspace}, nil
}

func (s *Service) UpdateWorkspace(_ context.Context, req *pb.UpdateWorkspaceReq) (*pb.UpdateWorkspaceRsp, error) {
	workspace := req.GetWorkspace()
	if workspace == nil || workspace.GetWorkspaceId() == "" {
		return &pb.UpdateWorkspaceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("workspace_id is required"))}, nil
	}
	s.mu.Lock()
	s.workspaces[workspace.GetWorkspaceId()] = workspace
	s.mu.Unlock()
	return &pb.UpdateWorkspaceRsp{RetInfo: quantstore.Success("success"), Workspace: workspace}, nil
}

func (s *Service) GetWorkspace(_ context.Context, req *pb.GetWorkspaceReq) (*pb.GetWorkspaceRsp, error) {
	s.mu.RLock()
	workspace := s.workspaces[req.GetWorkspaceId()]
	s.mu.RUnlock()
	if workspace == nil {
		return &pb.GetWorkspaceRsp{RetInfo: quantstore.Error(pb.ErrorCode_WORKSPACE_NOT_FOUND, fmt.Errorf("workspace %s not found", req.GetWorkspaceId()))}, nil
	}
	return &pb.GetWorkspaceRsp{RetInfo: quantstore.Success("success"), Workspace: workspace}, nil
}

func (s *Service) ListWorkspaces(_ context.Context, req *pb.ListWorkspacesReq) (*pb.ListWorkspacesRsp, error) {
	s.mu.RLock()
	items := values(s.workspaces)
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].GetWorkspaceId() < items[j].GetWorkspaceId() })
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListWorkspacesRsp{RetInfo: quantstore.Success("success"), Workspaces: paged, PageResult: page}, nil
}

func (s *Service) CreateExchange(_ context.Context, req *pb.CreateExchangeReq) (*pb.CreateExchangeRsp, error) {
	exchange := req.GetExchange()
	if exchange == nil {
		return &pb.CreateExchangeRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("exchange is required"))}, nil
	}
	if exchange.ExchangeId == "" {
		exchange.ExchangeId = defaultID(exchange.GetCode(), "exchange")
	}
	s.mu.Lock()
	s.exchanges[exchange.GetExchangeId()] = exchange
	s.mu.Unlock()
	return &pb.CreateExchangeRsp{RetInfo: quantstore.Success("success"), Exchange: exchange}, nil
}

func (s *Service) UpdateExchange(_ context.Context, req *pb.UpdateExchangeReq) (*pb.UpdateExchangeRsp, error) {
	exchange := req.GetExchange()
	if exchange == nil || exchange.GetExchangeId() == "" {
		return &pb.UpdateExchangeRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("exchange_id is required"))}, nil
	}
	s.mu.Lock()
	s.exchanges[exchange.GetExchangeId()] = exchange
	s.mu.Unlock()
	return &pb.UpdateExchangeRsp{RetInfo: quantstore.Success("success"), Exchange: exchange}, nil
}

func (s *Service) GetExchange(_ context.Context, req *pb.GetExchangeReq) (*pb.GetExchangeRsp, error) {
	s.mu.RLock()
	exchange := s.exchanges[req.GetExchangeId()]
	s.mu.RUnlock()
	if exchange == nil {
		return &pb.GetExchangeRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, fmt.Errorf("exchange %s not found", req.GetExchangeId()))}, nil
	}
	return &pb.GetExchangeRsp{RetInfo: quantstore.Success("success"), Exchange: exchange}, nil
}

func (s *Service) ListExchanges(_ context.Context, req *pb.ListExchangesReq) (*pb.ListExchangesRsp, error) {
	s.mu.RLock()
	var items []*pb.Exchange
	for _, item := range s.exchanges {
		if req.GetMarket() == "" || item.GetMarket() == req.GetMarket() {
			items = append(items, item)
		}
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].GetExchangeId() < items[j].GetExchangeId() })
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListExchangesRsp{RetInfo: quantstore.Success("success"), Exchanges: paged, PageResult: page}, nil
}

func (s *Service) UpsertInstrument(_ context.Context, req *pb.UpsertInstrumentReq) (*pb.UpsertInstrumentRsp, error) {
	instrument := req.GetInstrument()
	if instrument == nil {
		return &pb.UpsertInstrumentRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("instrument is required"))}, nil
	}
	if instrument.InstrumentId == "" {
		instrument.InstrumentId = defaultID(instrument.GetInternalSymbol(), "instrument")
	}
	s.mu.Lock()
	s.instruments[instrument.GetInstrumentId()] = instrument
	s.mu.Unlock()
	return &pb.UpsertInstrumentRsp{RetInfo: quantstore.Success("success"), Instrument: instrument}, nil
}

func (s *Service) GetInstrument(_ context.Context, req *pb.GetInstrumentReq) (*pb.GetInstrumentRsp, error) {
	s.mu.RLock()
	instrument := s.instruments[req.GetInstrumentId()]
	s.mu.RUnlock()
	if instrument == nil {
		return &pb.GetInstrumentRsp{RetInfo: quantstore.Error(pb.ErrorCode_INSTRUMENT_NOT_FOUND, fmt.Errorf("instrument %s not found", req.GetInstrumentId()))}, nil
	}
	return &pb.GetInstrumentRsp{RetInfo: quantstore.Success("success"), Instrument: instrument}, nil
}

func (s *Service) ListInstruments(_ context.Context, req *pb.ListInstrumentsReq) (*pb.ListInstrumentsRsp, error) {
	allowIDs := make(map[string]bool, len(req.GetInstrumentIds()))
	for _, id := range req.GetInstrumentIds() {
		allowIDs[id] = true
	}
	s.mu.RLock()
	var items []*pb.Instrument
	for _, item := range s.instruments {
		if len(allowIDs) > 0 && !allowIDs[item.GetInstrumentId()] {
			continue
		}
		if req.GetExchangeId() != "" && item.GetExchangeId() != req.GetExchangeId() {
			continue
		}
		if req.GetMarket() != "" && item.GetMarket() != req.GetMarket() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	sort.Slice(items, func(i, j int) bool { return items[i].GetInstrumentId() < items[j].GetInstrumentId() })
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListInstrumentsRsp{RetInfo: quantstore.Success("success"), Instruments: paged, PageResult: page}, nil
}

func (s *Service) UpsertInstrumentAlias(_ context.Context, req *pb.UpsertInstrumentAliasReq) (*pb.UpsertInstrumentAliasRsp, error) {
	alias := req.GetAlias()
	if alias == nil || alias.GetInstrumentId() == "" || alias.GetExternalSymbol() == "" {
		return &pb.UpsertInstrumentAliasRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("instrument_id and external_symbol are required"))}, nil
	}
	key := strings.Join([]string{alias.GetInstrumentId(), alias.GetDataSource(), alias.GetExchangeId(), alias.GetExternalSymbol()}, "|")
	s.mu.Lock()
	s.aliases[key] = alias
	s.mu.Unlock()
	return &pb.UpsertInstrumentAliasRsp{RetInfo: quantstore.Success("success"), Alias: alias}, nil
}

func (s *Service) ListInstrumentAliases(_ context.Context, req *pb.ListInstrumentAliasesReq) (*pb.ListInstrumentAliasesRsp, error) {
	s.mu.RLock()
	var items []*pb.InstrumentAlias
	for _, item := range s.aliases {
		if req.GetInstrumentId() != "" && item.GetInstrumentId() != req.GetInstrumentId() {
			continue
		}
		if req.GetDataSource() != "" && item.GetDataSource() != req.GetDataSource() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListInstrumentAliasesRsp{RetInfo: quantstore.Success("success"), Aliases: paged, PageResult: page}, nil
}

func (s *Service) CreateDataSet(_ context.Context, req *pb.CreateDataSetReq) (*pb.CreateDataSetRsp, error) {
	dataset := req.GetDataset()
	if dataset == nil || dataset.GetWorkspaceId() == "" {
		return &pb.CreateDataSetRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("dataset.workspace_id is required"))}, nil
	}
	if dataset.DatasetId == "" {
		dataset.DatasetId = defaultID(dataset.GetName(), "dataset")
	}
	s.mu.Lock()
	s.datasets[datasetKey(dataset.GetWorkspaceId(), dataset.GetDatasetId())] = dataset
	s.mu.Unlock()
	return &pb.CreateDataSetRsp{RetInfo: quantstore.Success("success"), Dataset: dataset}, nil
}

func (s *Service) UpdateDataSet(_ context.Context, req *pb.UpdateDataSetReq) (*pb.UpdateDataSetRsp, error) {
	dataset := req.GetDataset()
	if dataset == nil || dataset.GetWorkspaceId() == "" || dataset.GetDatasetId() == "" {
		return &pb.UpdateDataSetRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("workspace_id and dataset_id are required"))}, nil
	}
	s.mu.Lock()
	s.datasets[datasetKey(dataset.GetWorkspaceId(), dataset.GetDatasetId())] = dataset
	s.mu.Unlock()
	return &pb.UpdateDataSetRsp{RetInfo: quantstore.Success("success"), Dataset: dataset}, nil
}

func (s *Service) GetDataSet(_ context.Context, req *pb.GetDataSetReq) (*pb.GetDataSetRsp, error) {
	s.mu.RLock()
	dataset := s.datasets[datasetKey(req.GetWorkspaceId(), req.GetDatasetId())]
	s.mu.RUnlock()
	if dataset == nil {
		return &pb.GetDataSetRsp{RetInfo: quantstore.Error(pb.ErrorCode_DATASET_NOT_FOUND, fmt.Errorf("dataset %s not found", req.GetDatasetId()))}, nil
	}
	return &pb.GetDataSetRsp{RetInfo: quantstore.Success("success"), Dataset: dataset}, nil
}

func (s *Service) ListDataSets(_ context.Context, req *pb.ListDataSetsReq) (*pb.ListDataSetsRsp, error) {
	s.mu.RLock()
	var items []*pb.DataSet
	for _, item := range s.datasets {
		if req.GetWorkspaceId() != "" && item.GetWorkspaceId() != req.GetWorkspaceId() {
			continue
		}
		if req.GetDataKind() != pb.DataKind_DATA_KIND_UNSPECIFIED && item.GetDataKind() != req.GetDataKind() {
			continue
		}
		if req.GetDataDomain() != pb.DataDomain_DATA_DOMAIN_UNSPECIFIED && item.GetDataDomain() != req.GetDataDomain() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListDataSetsRsp{RetInfo: quantstore.Success("success"), Datasets: paged, PageResult: page}, nil
}

func (s *Service) CreateField(_ context.Context, req *pb.CreateFieldReq) (*pb.CreateFieldRsp, error) {
	field := req.GetField()
	if field == nil || field.GetWorkspaceId() == "" || field.GetDatasetId() == "" {
		return &pb.CreateFieldRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("field workspace_id and dataset_id are required"))}, nil
	}
	if field.FieldId == "" {
		field.FieldId = defaultID(field.GetInterfaceName(), "field")
	}
	s.mu.Lock()
	s.fields[fieldKey(field.GetWorkspaceId(), field.GetDatasetId(), field.GetFieldId())] = field
	s.mu.Unlock()
	return &pb.CreateFieldRsp{RetInfo: quantstore.Success("success"), Field: field}, nil
}

func (s *Service) UpdateField(_ context.Context, req *pb.UpdateFieldReq) (*pb.UpdateFieldRsp, error) {
	field := req.GetField()
	if field == nil || field.GetWorkspaceId() == "" || field.GetDatasetId() == "" || field.GetFieldId() == "" {
		return &pb.UpdateFieldRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("field workspace_id, dataset_id and field_id are required"))}, nil
	}
	s.mu.Lock()
	s.fields[fieldKey(field.GetWorkspaceId(), field.GetDatasetId(), field.GetFieldId())] = field
	s.mu.Unlock()
	return &pb.UpdateFieldRsp{RetInfo: quantstore.Success("success"), Field: field}, nil
}

func (s *Service) GetField(_ context.Context, req *pb.GetFieldReq) (*pb.GetFieldRsp, error) {
	s.mu.RLock()
	var field *pb.Field
	if req.GetFieldId() != "" {
		field = s.fields[fieldKey(req.GetWorkspaceId(), req.GetDatasetId(), req.GetFieldId())]
	} else {
		for _, item := range s.fields {
			if item.GetWorkspaceId() == req.GetWorkspaceId() && item.GetDatasetId() == req.GetDatasetId() && item.GetInterfaceName() == req.GetInterfaceName() {
				field = item
				break
			}
		}
	}
	s.mu.RUnlock()
	if field == nil {
		return &pb.GetFieldRsp{RetInfo: quantstore.Error(pb.ErrorCode_FIELD_NOT_FOUND, errors.New("field not found"))}, nil
	}
	return &pb.GetFieldRsp{RetInfo: quantstore.Success("success"), Field: field}, nil
}

func (s *Service) ListFields(_ context.Context, req *pb.ListFieldsReq) (*pb.ListFieldsRsp, error) {
	s.mu.RLock()
	var items []*pb.Field
	for _, item := range s.fields {
		if req.GetWorkspaceId() != "" && item.GetWorkspaceId() != req.GetWorkspaceId() {
			continue
		}
		if req.GetDatasetId() != "" && item.GetDatasetId() != req.GetDatasetId() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListFieldsRsp{RetInfo: quantstore.Success("success"), Fields: paged, PageResult: page}, nil
}

func (s *Service) CreateFactorDef(_ context.Context, req *pb.CreateFactorDefReq) (*pb.CreateFactorDefRsp, error) {
	item := req.GetFactorDef()
	if item == nil || item.GetWorkspaceId() == "" {
		return &pb.CreateFactorDefRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("factor_def workspace_id is required"))}, nil
	}
	if item.FactorDefId == "" {
		item.FactorDefId = defaultID(item.GetName(), "factor_def")
	}
	s.mu.Lock()
	s.factorDefs[workspaceKey(item.GetWorkspaceId(), item.GetFactorDefId())] = item
	s.mu.Unlock()
	return &pb.CreateFactorDefRsp{RetInfo: quantstore.Success("success"), FactorDef: item}, nil
}

func (s *Service) UpdateFactorDef(_ context.Context, req *pb.UpdateFactorDefReq) (*pb.UpdateFactorDefRsp, error) {
	item := req.GetFactorDef()
	if item == nil || item.GetWorkspaceId() == "" || item.GetFactorDefId() == "" {
		return &pb.UpdateFactorDefRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("factor_def workspace_id and factor_def_id are required"))}, nil
	}
	s.mu.Lock()
	s.factorDefs[workspaceKey(item.GetWorkspaceId(), item.GetFactorDefId())] = item
	s.mu.Unlock()
	return &pb.UpdateFactorDefRsp{RetInfo: quantstore.Success("success"), FactorDef: item}, nil
}

func (s *Service) GetFactorDef(_ context.Context, req *pb.GetFactorDefReq) (*pb.GetFactorDefRsp, error) {
	s.mu.RLock()
	item := s.factorDefs[workspaceKey(req.GetWorkspaceId(), req.GetFactorDefId())]
	s.mu.RUnlock()
	if item == nil {
		return &pb.GetFactorDefRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("factor_def not found"))}, nil
	}
	return &pb.GetFactorDefRsp{RetInfo: quantstore.Success("success"), FactorDef: item}, nil
}

func (s *Service) ListFactorDefs(_ context.Context, req *pb.ListFactorDefsReq) (*pb.ListFactorDefsRsp, error) {
	s.mu.RLock()
	var items []*pb.FactorDef
	for _, item := range s.factorDefs {
		if req.GetWorkspaceId() == "" || item.GetWorkspaceId() == req.GetWorkspaceId() {
			items = append(items, item)
		}
	}
	s.mu.RUnlock()
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListFactorDefsRsp{RetInfo: quantstore.Success("success"), FactorDefs: paged, PageResult: page}, nil
}

func (s *Service) CreateFactorInstance(_ context.Context, req *pb.CreateFactorInstanceReq) (*pb.CreateFactorInstanceRsp, error) {
	item := req.GetFactorInstance()
	if item == nil || item.GetWorkspaceId() == "" {
		return &pb.CreateFactorInstanceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("factor_instance workspace_id is required"))}, nil
	}
	if item.FactorInstanceId == "" {
		item.FactorInstanceId = defaultID(item.GetName(), "factor_instance")
	}
	s.mu.Lock()
	s.factorInstances[workspaceKey(item.GetWorkspaceId(), item.GetFactorInstanceId())] = item
	s.mu.Unlock()
	return &pb.CreateFactorInstanceRsp{RetInfo: quantstore.Success("success"), FactorInstance: item}, nil
}

func (s *Service) UpdateFactorInstance(_ context.Context, req *pb.UpdateFactorInstanceReq) (*pb.UpdateFactorInstanceRsp, error) {
	item := req.GetFactorInstance()
	if item == nil || item.GetWorkspaceId() == "" || item.GetFactorInstanceId() == "" {
		return &pb.UpdateFactorInstanceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("factor_instance workspace_id and factor_instance_id are required"))}, nil
	}
	s.mu.Lock()
	s.factorInstances[workspaceKey(item.GetWorkspaceId(), item.GetFactorInstanceId())] = item
	s.mu.Unlock()
	return &pb.UpdateFactorInstanceRsp{RetInfo: quantstore.Success("success"), FactorInstance: item}, nil
}

func (s *Service) GetFactorInstance(_ context.Context, req *pb.GetFactorInstanceReq) (*pb.GetFactorInstanceRsp, error) {
	s.mu.RLock()
	item := s.factorInstances[workspaceKey(req.GetWorkspaceId(), req.GetFactorInstanceId())]
	s.mu.RUnlock()
	if item == nil {
		return &pb.GetFactorInstanceRsp{RetInfo: quantstore.Error(pb.ErrorCode_FACTOR_INSTANCE_NOT_FOUND, errors.New("factor_instance not found"))}, nil
	}
	return &pb.GetFactorInstanceRsp{RetInfo: quantstore.Success("success"), FactorInstance: item}, nil
}

func (s *Service) ListFactorInstances(_ context.Context, req *pb.ListFactorInstancesReq) (*pb.ListFactorInstancesRsp, error) {
	s.mu.RLock()
	var items []*pb.FactorInstance
	for _, item := range s.factorInstances {
		if req.GetWorkspaceId() != "" && item.GetWorkspaceId() != req.GetWorkspaceId() {
			continue
		}
		if req.GetFactorDefId() != "" && item.GetFactorDefId() != req.GetFactorDefId() {
			continue
		}
		if req.GetDatasetId() != "" && item.GetDatasetId() != req.GetDatasetId() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListFactorInstancesRsp{RetInfo: quantstore.Success("success"), FactorInstances: paged, PageResult: page}, nil
}

func (s *Service) CreateDataView(_ context.Context, req *pb.CreateDataViewReq) (*pb.CreateDataViewRsp, error) {
	item := req.GetDataView()
	if item == nil || item.GetWorkspaceId() == "" {
		return &pb.CreateDataViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("data_view workspace_id is required"))}, nil
	}
	if item.DataViewId == "" {
		item.DataViewId = defaultID(item.GetName(), "data_view")
	}
	s.mu.Lock()
	s.dataViews[workspaceKey(item.GetWorkspaceId(), item.GetDataViewId())] = item
	s.mu.Unlock()
	return &pb.CreateDataViewRsp{RetInfo: quantstore.Success("success"), DataView: item}, nil
}

func (s *Service) UpdateDataView(_ context.Context, req *pb.UpdateDataViewReq) (*pb.UpdateDataViewRsp, error) {
	item := req.GetDataView()
	if item == nil || item.GetWorkspaceId() == "" || item.GetDataViewId() == "" {
		return &pb.UpdateDataViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("data_view workspace_id and data_view_id are required"))}, nil
	}
	s.mu.Lock()
	s.dataViews[workspaceKey(item.GetWorkspaceId(), item.GetDataViewId())] = item
	s.mu.Unlock()
	return &pb.UpdateDataViewRsp{RetInfo: quantstore.Success("success"), DataView: item}, nil
}

func (s *Service) GetDataView(_ context.Context, req *pb.GetDataViewReq) (*pb.GetDataViewRsp, error) {
	s.mu.RLock()
	item := s.dataViews[workspaceKey(req.GetWorkspaceId(), req.GetDataViewId())]
	s.mu.RUnlock()
	if item == nil {
		return &pb.GetDataViewRsp{RetInfo: quantstore.Error(pb.ErrorCode_DATA_VIEW_NOT_READY, errors.New("data_view not found"))}, nil
	}
	return &pb.GetDataViewRsp{RetInfo: quantstore.Success("success"), DataView: item}, nil
}

func (s *Service) ListDataViews(_ context.Context, req *pb.ListDataViewsReq) (*pb.ListDataViewsRsp, error) {
	s.mu.RLock()
	var items []*pb.DataView
	for _, item := range s.dataViews {
		if req.GetWorkspaceId() == "" || item.GetWorkspaceId() == req.GetWorkspaceId() {
			items = append(items, item)
		}
	}
	s.mu.RUnlock()
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListDataViewsRsp{RetInfo: quantstore.Success("success"), DataViews: paged, PageResult: page}, nil
}

func (s *Service) CreateStorageDevice(_ context.Context, req *pb.CreateStorageDeviceReq) (*pb.CreateStorageDeviceRsp, error) {
	item := req.GetStorageDevice()
	if item == nil {
		return &pb.CreateStorageDeviceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("storage_device is required"))}, nil
	}
	if item.DeviceId == "" {
		item.DeviceId = defaultID(item.GetName(), "device")
	}
	s.mu.Lock()
	s.storageDevices[item.GetDeviceId()] = item
	s.mu.Unlock()
	return &pb.CreateStorageDeviceRsp{RetInfo: quantstore.Success("success"), StorageDevice: item}, nil
}

func (s *Service) UpdateStorageDevice(_ context.Context, req *pb.UpdateStorageDeviceReq) (*pb.UpdateStorageDeviceRsp, error) {
	item := req.GetStorageDevice()
	if item == nil || item.GetDeviceId() == "" {
		return &pb.UpdateStorageDeviceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("device_id is required"))}, nil
	}
	s.mu.Lock()
	s.storageDevices[item.GetDeviceId()] = item
	s.mu.Unlock()
	return &pb.UpdateStorageDeviceRsp{RetInfo: quantstore.Success("success"), StorageDevice: item}, nil
}

func (s *Service) GetStorageDevice(_ context.Context, req *pb.GetStorageDeviceReq) (*pb.GetStorageDeviceRsp, error) {
	s.mu.RLock()
	item := s.storageDevices[req.GetDeviceId()]
	s.mu.RUnlock()
	if item == nil {
		return &pb.GetStorageDeviceRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("storage_device not found"))}, nil
	}
	return &pb.GetStorageDeviceRsp{RetInfo: quantstore.Success("success"), StorageDevice: item}, nil
}

func (s *Service) ListStorageDevices(_ context.Context, req *pb.ListStorageDevicesReq) (*pb.ListStorageDevicesRsp, error) {
	s.mu.RLock()
	items := values(s.storageDevices)
	s.mu.RUnlock()
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListStorageDevicesRsp{RetInfo: quantstore.Success("success"), StorageDevices: paged, PageResult: page}, nil
}

func (s *Service) CreateStorageRoute(_ context.Context, req *pb.CreateStorageRouteReq) (*pb.CreateStorageRouteRsp, error) {
	item := req.GetStorageRoute()
	if item == nil || item.GetWorkspaceId() == "" || item.GetDatasetId() == "" {
		return &pb.CreateStorageRouteRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("route workspace_id and dataset_id are required"))}, nil
	}
	if item.RouteId == "" {
		item.RouteId = defaultID(item.GetDatasetId()+"-"+item.GetDeviceId(), "route")
	}
	s.mu.Lock()
	s.storageRoutes[item.GetRouteId()] = item
	s.mu.Unlock()
	return &pb.CreateStorageRouteRsp{RetInfo: quantstore.Success("success"), StorageRoute: item}, nil
}

func (s *Service) UpdateStorageRoute(_ context.Context, req *pb.UpdateStorageRouteReq) (*pb.UpdateStorageRouteRsp, error) {
	item := req.GetStorageRoute()
	if item == nil || item.GetRouteId() == "" {
		return &pb.UpdateStorageRouteRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("route_id is required"))}, nil
	}
	s.mu.Lock()
	s.storageRoutes[item.GetRouteId()] = item
	s.mu.Unlock()
	return &pb.UpdateStorageRouteRsp{RetInfo: quantstore.Success("success"), StorageRoute: item}, nil
}

func (s *Service) GetStorageRoute(_ context.Context, req *pb.GetStorageRouteReq) (*pb.GetStorageRouteRsp, error) {
	s.mu.RLock()
	item := s.storageRoutes[req.GetRouteId()]
	s.mu.RUnlock()
	if item == nil {
		return &pb.GetStorageRouteRsp{RetInfo: quantstore.Error(pb.ErrorCode_ROUTE_NOT_FOUND, errors.New("storage_route not found"))}, nil
	}
	return &pb.GetStorageRouteRsp{RetInfo: quantstore.Success("success"), StorageRoute: item}, nil
}

func (s *Service) ListStorageRoutes(_ context.Context, req *pb.ListStorageRoutesReq) (*pb.ListStorageRoutesRsp, error) {
	s.mu.RLock()
	var items []*pb.StorageRoute
	for _, item := range s.storageRoutes {
		if req.GetWorkspaceId() != "" && item.GetWorkspaceId() != req.GetWorkspaceId() {
			continue
		}
		if req.GetDatasetId() != "" && item.GetDatasetId() != req.GetDatasetId() {
			continue
		}
		if req.GetDeviceId() != "" && item.GetDeviceId() != req.GetDeviceId() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListStorageRoutesRsp{RetInfo: quantstore.Success("success"), StorageRoutes: paged, PageResult: page}, nil
}

func (s *Service) ConfigureCollectorDataSetBinding(_ context.Context, req *pb.ConfigureCollectorDataSetBindingReq) (*pb.ConfigureCollectorDataSetBindingRsp, error) {
	item := req.GetBinding()
	if item == nil || item.GetWorkspaceId() == "" || item.GetDatasetId() == "" {
		return &pb.ConfigureCollectorDataSetBindingRsp{RetInfo: quantstore.Error(pb.ErrorCode_INVALID_PARAM, errors.New("binding workspace_id and dataset_id are required"))}, nil
	}
	if item.BindingId == "" {
		item.BindingId = defaultID(item.GetDatasetId()+"-"+item.GetDataSource(), "binding")
	}
	s.mu.Lock()
	s.collectorBindings[item.GetBindingId()] = item
	s.mu.Unlock()
	return &pb.ConfigureCollectorDataSetBindingRsp{RetInfo: quantstore.Success("success"), Binding: item}, nil
}

func (s *Service) ListCollectorDataSetBindings(_ context.Context, req *pb.ListCollectorDataSetBindingsReq) (*pb.ListCollectorDataSetBindingsRsp, error) {
	s.mu.RLock()
	var items []*pb.CollectorDataSetBinding
	for _, item := range s.collectorBindings {
		if req.GetWorkspaceId() != "" && item.GetWorkspaceId() != req.GetWorkspaceId() {
			continue
		}
		if req.GetDatasetId() != "" && item.GetDatasetId() != req.GetDatasetId() {
			continue
		}
		if req.GetDataSource() != "" && item.GetDataSource() != req.GetDataSource() {
			continue
		}
		items = append(items, item)
	}
	s.mu.RUnlock()
	paged, page := pageSlice(items, req.GetPage())
	return &pb.ListCollectorDataSetBindingsRsp{RetInfo: quantstore.Success("success"), Bindings: paged, PageResult: page}, nil
}

func datasetKey(workspaceID, datasetID string) string {
	return workspaceID + "|" + datasetID
}

func workspaceKey(workspaceID, id string) string {
	return workspaceID + "|" + id
}

func fieldKey(workspaceID, datasetID, fieldID string) string {
	return workspaceID + "|" + datasetID + "|" + fieldID
}

func defaultID(name, prefix string) string {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return prefix + "_" + xid.New().String()
	}
	replacer := strings.NewReplacer(" ", "_", "/", "_", "\\", "_", ":", "_")
	return replacer.Replace(name)
}

func values[T any](m map[string]T) []T {
	items := make([]T, 0, len(m))
	for _, item := range m {
		items = append(items, item)
	}
	return items
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
