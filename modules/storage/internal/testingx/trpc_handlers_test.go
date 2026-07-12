package testingx

import (
	"context"
	"testing"

	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/require"
	"trpc.group/trpc-go/trpc-go/filter"
	"trpc.group/trpc-go/trpc-go/server"
)

type trpcHandler func(interface{}, context.Context, server.FilterFunc) (interface{}, error)

func TestAllGeneratedTRPCHandlersInvokeUnimplementedServices(t *testing.T) {
	ctx := context.Background()
	noop := func(req interface{}) (filter.ServerChain, error) { return nil, nil }
	cases := []struct {
		svc interface{}
		fn  trpcHandler
	}{
		{&pb.UnimplementedAccess{}, pb.AccessService_WriteTimeSeriesRows_Handler},
		{&pb.UnimplementedAccess{}, pb.AccessService_ReadTimeSeriesRows_Handler},
		{&pb.UnimplementedAccess{}, pb.AccessService_DeleteTimeSeriesRows_Handler},
		{&pb.UnimplementedAccess{}, pb.AccessService_WriteRecordRows_Handler},
		{&pb.UnimplementedAccess{}, pb.AccessService_ReadRecordRows_Handler},
		{&pb.UnimplementedAccessScan{}, pb.AccessScanService_ScanTimeSeriesRows_Handler},
		{&pb.UnimplementedAccessScan{}, pb.AccessScanService_ScanRecordRows_Handler},
		{&pb.UnimplementedPrimaryStore{}, pb.PrimaryStoreService_WritePrimaryRows_Handler},
		{&pb.UnimplementedPrimaryStore{}, pb.PrimaryStoreService_ReadPrimaryRows_Handler},
		{&pb.UnimplementedPrimaryStore{}, pb.PrimaryStoreService_ScanPrimaryRows_Handler},
		{&pb.UnimplementedPrimaryStore{}, pb.PrimaryStoreService_DeletePrimaryRows_Handler},
		{&pb.UnimplementedDataView{}, pb.DataViewService_QueryTimeSeriesRows_Handler},
		{&pb.UnimplementedDataView{}, pb.DataViewService_SearchRecordRows_Handler},
		{&pb.UnimplementedViewIndex{}, pb.ViewIndexService_PrepareViewIndex_Handler},
		{&pb.UnimplementedViewIndex{}, pb.ViewIndexService_WriteViewIndex_Handler},
		{&pb.UnimplementedViewIndex{}, pb.ViewIndexService_StatViewIndex_Handler},
		{&pb.UnimplementedViewIndex{}, pb.ViewIndexService_RemoveViewIndex_Handler},
		{&pb.UnimplementedViewIndex{}, pb.ViewIndexService_ListViewIndexes_Handler},
		{&pb.UnimplementedViewIndex{}, pb.ViewIndexService_QueryTimeSeriesIndex_Handler},
		{&pb.UnimplementedViewIndex{}, pb.ViewIndexService_SearchRecordIndex_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_CreateSpace_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_UpdateSpace_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_GetSpace_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ListSpaces_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_CreateView_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_UpdateView_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_GetView_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ListViews_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_UpsertViewColumn_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ListViewColumns_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ClaimViewIndexBuild_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_UpdateViewIndexBuild_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ActivateViewIndex_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_FailViewIndexBuild_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_CreateDataSource_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_UpdateDataSource_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_GetDataSource_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ListDataSources_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_UpsertSubject_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_UpsertSubjectSymbol_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_RegisterDataSubject_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_GetSubject_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ListSubjects_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ListSubjectSymbols_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_CreateDataset_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_UpdateDataset_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_GetDataset_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ListDatasets_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_BindDatasetSubject_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ListDatasetSubjects_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_CreateField_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_UpdateField_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_GetField_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ListFields_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_CreateFactor_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_UpdateFactor_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_GetFactor_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ListFactors_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_UpsertDatasetColumn_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ListDatasetColumns_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_CreatePrimaryStoreNode_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_UpdatePrimaryStoreNode_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_GetPrimaryStoreNode_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ListPrimaryStoreNodes_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_CreateDevice_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_UpdateDevice_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_GetDevice_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ListDevices_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_CreatePrimaryStoreRoute_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_UpdatePrimaryStoreRoute_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_GetPrimaryStoreRoute_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ListPrimaryStoreRoutes_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_RegisterArchiveFile_Handler},
		{&pb.UnimplementedMetadata{}, pb.MetadataService_ListArchiveFiles_Handler},
	}
	for _, tc := range cases {
		_, err := tc.fn(tc.svc, ctx, noop)
		require.Error(t, err)
	}
}

func TestGeneratedTRPCServicesRegisterOnServer(t *testing.T) {
	s := server.New()
	pb.RegisterMetadataService(s, &pb.UnimplementedMetadata{})
	pb.RegisterAccessService(s, &pb.UnimplementedAccess{})
	pb.RegisterAccessScanService(s, &pb.UnimplementedAccessScan{})
	pb.RegisterPrimaryStoreService(s, &pb.UnimplementedPrimaryStore{})
	pb.RegisterDataViewService(s, &pb.UnimplementedDataView{})
	pb.RegisterViewIndexService(s, &pb.UnimplementedViewIndex{})
}

func TestGeneratedTRPCClientProxiesConstruct(t *testing.T) {
	_ = pb.NewMetadataClientProxy()
	_ = pb.NewAccessClientProxy()
	_ = pb.NewAccessScanClientProxy()
	_ = pb.NewPrimaryStoreClientProxy()
	_ = pb.NewDataViewClientProxy()
	_ = pb.NewViewIndexClientProxy()
}
