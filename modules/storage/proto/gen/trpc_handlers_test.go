package storagepb

import (
	"context"
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/server"
)

func trpcNoopFilter(req interface{}) (filter.ServerChain, error) {
	return filter.ServerChain{filter.NoopServerFilter}, nil
}

func dispatchStorageHandler(t *testing.T, handler interface{}, svr interface{}) {
	t.Helper()
	hv := reflect.ValueOf(handler)
	out := hv.Call([]reflect.Value{
		reflect.ValueOf(svr),
		reflect.ValueOf(context.Background()),
		reflect.ValueOf(server.FilterFunc(trpcNoopFilter)),
	})
	require.Len(t, out, 2)
	if err, ok := out[1].Interface().(error); ok && err != nil {
		assert.Error(t, err)
	}
}

func TestAllStorageTRPCHandlers_ShouldExecute(t *testing.T) {
	metadata := &UnimplementedMetadata{}
	access := &UnimplementedAccess{}
	accessScan := &UnimplementedAccessScan{}
	primary := &UnimplementedPrimaryStore{}
	view := &UnimplementedDataView{}
	viewIndex := &UnimplementedViewIndex{}

	cases := []struct {
		name    string
		handler interface{}
		svr     interface{}
	}{
		{"MetadataCreateSpace", MetadataService_CreateSpace_Handler, metadata},
		{"MetadataUpdateSpace", MetadataService_UpdateSpace_Handler, metadata},
		{"MetadataGetSpace", MetadataService_GetSpace_Handler, metadata},
		{"MetadataListSpaces", MetadataService_ListSpaces_Handler, metadata},
		{"MetadataCreateView", MetadataService_CreateView_Handler, metadata},
		{"MetadataUpdateView", MetadataService_UpdateView_Handler, metadata},
		{"MetadataGetView", MetadataService_GetView_Handler, metadata},
		{"MetadataListViews", MetadataService_ListViews_Handler, metadata},
		{"MetadataUpsertViewColumn", MetadataService_UpsertViewColumn_Handler, metadata},
		{"MetadataListViewColumns", MetadataService_ListViewColumns_Handler, metadata},
		{"MetadataClaimViewIndexBuild", MetadataService_ClaimViewIndexBuild_Handler, metadata},
		{"MetadataUpdateViewIndexBuild", MetadataService_UpdateViewIndexBuild_Handler, metadata},
		{"MetadataActivateViewIndex", MetadataService_ActivateViewIndex_Handler, metadata},
		{"MetadataFailViewIndexBuild", MetadataService_FailViewIndexBuild_Handler, metadata},
		{"MetadataCreateDataSource", MetadataService_CreateDataSource_Handler, metadata},
		{"MetadataUpdateDataSource", MetadataService_UpdateDataSource_Handler, metadata},
		{"MetadataGetDataSource", MetadataService_GetDataSource_Handler, metadata},
		{"MetadataListDataSources", MetadataService_ListDataSources_Handler, metadata},
		{"MetadataUpsertSubject", MetadataService_UpsertSubject_Handler, metadata},
		{"MetadataUpsertSubjectSymbol", MetadataService_UpsertSubjectSymbol_Handler, metadata},
		{"MetadataRegisterDataSubject", MetadataService_RegisterDataSubject_Handler, metadata},
		{"MetadataGetSubject", MetadataService_GetSubject_Handler, metadata},
		{"MetadataListSubjects", MetadataService_ListSubjects_Handler, metadata},
		{"MetadataListSubjectSymbols", MetadataService_ListSubjectSymbols_Handler, metadata},
		{"MetadataCreateDataset", MetadataService_CreateDataset_Handler, metadata},
		{"MetadataUpdateDataset", MetadataService_UpdateDataset_Handler, metadata},
		{"MetadataGetDataset", MetadataService_GetDataset_Handler, metadata},
		{"MetadataListDatasets", MetadataService_ListDatasets_Handler, metadata},
		{"MetadataBindDatasetSubject", MetadataService_BindDatasetSubject_Handler, metadata},
		{"MetadataListDatasetSubjects", MetadataService_ListDatasetSubjects_Handler, metadata},
		{"MetadataCreateField", MetadataService_CreateField_Handler, metadata},
		{"MetadataUpdateField", MetadataService_UpdateField_Handler, metadata},
		{"MetadataGetField", MetadataService_GetField_Handler, metadata},
		{"MetadataListFields", MetadataService_ListFields_Handler, metadata},
		{"MetadataCreateFactor", MetadataService_CreateFactor_Handler, metadata},
		{"MetadataUpdateFactor", MetadataService_UpdateFactor_Handler, metadata},
		{"MetadataGetFactor", MetadataService_GetFactor_Handler, metadata},
		{"MetadataListFactors", MetadataService_ListFactors_Handler, metadata},
		{"MetadataUpsertDatasetColumn", MetadataService_UpsertDatasetColumn_Handler, metadata},
		{"MetadataListDatasetColumns", MetadataService_ListDatasetColumns_Handler, metadata},
		{"MetadataCreatePrimaryStoreNode", MetadataService_CreatePrimaryStoreNode_Handler, metadata},
		{"MetadataUpdatePrimaryStoreNode", MetadataService_UpdatePrimaryStoreNode_Handler, metadata},
		{"MetadataGetPrimaryStoreNode", MetadataService_GetPrimaryStoreNode_Handler, metadata},
		{"MetadataListPrimaryStoreNodes", MetadataService_ListPrimaryStoreNodes_Handler, metadata},
		{"MetadataCreateDevice", MetadataService_CreateDevice_Handler, metadata},
		{"MetadataUpdateDevice", MetadataService_UpdateDevice_Handler, metadata},
		{"MetadataGetDevice", MetadataService_GetDevice_Handler, metadata},
		{"MetadataListDevices", MetadataService_ListDevices_Handler, metadata},
		{"MetadataCreatePrimaryStoreRoute", MetadataService_CreatePrimaryStoreRoute_Handler, metadata},
		{"MetadataUpdatePrimaryStoreRoute", MetadataService_UpdatePrimaryStoreRoute_Handler, metadata},
		{"MetadataGetPrimaryStoreRoute", MetadataService_GetPrimaryStoreRoute_Handler, metadata},
		{"MetadataListPrimaryStoreRoutes", MetadataService_ListPrimaryStoreRoutes_Handler, metadata},
		{"MetadataRegisterArchiveFile", MetadataService_RegisterArchiveFile_Handler, metadata},
		{"MetadataListArchiveFiles", MetadataService_ListArchiveFiles_Handler, metadata},
		{"AccessWriteTimeSeries", AccessService_WriteTimeSeriesRows_Handler, access},
		{"AccessReadTimeSeries", AccessService_ReadTimeSeriesRows_Handler, access},
		{"AccessDeleteTimeSeries", AccessService_DeleteTimeSeriesRows_Handler, access},
		{"AccessWriteRecord", AccessService_WriteRecordRows_Handler, access},
		{"AccessReadRecord", AccessService_ReadRecordRows_Handler, access},
		{"AccessScanTimeSeries", AccessScanService_ScanTimeSeriesRows_Handler, accessScan},
		{"AccessScanRecord", AccessScanService_ScanRecordRows_Handler, accessScan},
		{"PrimaryWrite", PrimaryStoreService_WritePrimaryRows_Handler, primary},
		{"PrimaryRead", PrimaryStoreService_ReadPrimaryRows_Handler, primary},
		{"PrimaryScan", PrimaryStoreService_ScanPrimaryRows_Handler, primary},
		{"PrimaryDelete", PrimaryStoreService_DeletePrimaryRows_Handler, primary},
		{"ViewQueryTimeSeries", DataViewService_QueryTimeSeriesRows_Handler, view},
		{"ViewSearchRecord", DataViewService_SearchRecordRows_Handler, view},
		{"ViewIndexPrepare", ViewIndexService_PrepareViewIndex_Handler, viewIndex},
		{"ViewIndexWrite", ViewIndexService_WriteViewIndex_Handler, viewIndex},
		{"ViewIndexStat", ViewIndexService_StatViewIndex_Handler, viewIndex},
		{"ViewIndexRemove", ViewIndexService_RemoveViewIndex_Handler, viewIndex},
		{"ViewIndexList", ViewIndexService_ListViewIndexes_Handler, viewIndex},
		{"ViewIndexQueryTimeSeries", ViewIndexService_QueryTimeSeriesIndex_Handler, viewIndex},
		{"ViewIndexSearchRecord", ViewIndexService_SearchRecordIndex_Handler, viewIndex},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dispatchStorageHandler(t, tc.handler, tc.svr)
		})
	}
}

type fakeStorageTRPCService struct{ registered bool }

func (f *fakeStorageTRPCService) Register(serviceDesc interface{}, serviceImpl interface{}) error {
	f.registered = true
	return nil
}
func (f *fakeStorageTRPCService) Serve() error              { return nil }
func (f *fakeStorageTRPCService) Close(chan struct{}) error { return nil }

func TestRegisterStorageServices_ShouldRegisterWithoutPanic(t *testing.T) {
	s := &fakeStorageTRPCService{}
	require.NotPanics(t, func() {
		RegisterMetadataService(s, &UnimplementedMetadata{})
		RegisterAccessService(s, &UnimplementedAccess{})
		RegisterAccessScanService(s, &UnimplementedAccessScan{})
		RegisterPrimaryStoreService(s, &UnimplementedPrimaryStore{})
		RegisterDataViewService(s, &UnimplementedDataView{})
		RegisterViewIndexService(s, &UnimplementedViewIndex{})
	})
	assert.True(t, s.registered)
}
