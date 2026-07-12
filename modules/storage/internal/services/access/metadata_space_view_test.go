package access

import (
	"context"
	"os"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/core/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/gen"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
)

func TestCreateSpaceRejectsMissingFields(t *testing.T) {
	svc := &Service{metadata: &stubMetadataStore{}}
	rsp, err := svc.CreateSpace(context.Background(), &pb.CreateSpaceReq{})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
}

func TestCreateSpacePersistsGeneratedID(t *testing.T) {
	store := &stubMetadataStore{}
	svc := &Service{metadata: store}
	rsp, err := svc.CreateSpace(context.Background(), &pb.CreateSpaceReq{Space: &pb.Space{Name: "Crypto"}})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.NotEmpty(t, rsp.GetSpace().GetSpaceId())
	assert.Equal(t, rsp.GetSpace().GetSpaceId(), store.lastSpace.GetSpaceId())
}

func TestGetSpaceReturnsStoredValue(t *testing.T) {
	store := &stubMetadataStore{
		spaces: map[string]*pb.Space{"crypto": {SpaceId: "crypto", Name: "Crypto"}},
	}
	svc := &Service{metadata: store}
	rsp, err := svc.GetSpace(context.Background(), &pb.GetSpaceReq{SpaceId: "crypto"})
	require.NoError(t, err)
	assert.Equal(t, pb.ErrorCode_SUCCESS, rsp.GetRetInfo().GetCode())
	assert.Equal(t, "Crypto", rsp.GetSpace().GetName())
}

func TestStorageRootUsesEnvOverride(t *testing.T) {
	t.Setenv("MOOX_STORAGE_HOME", "/tmp/moox-storage")
	assert.Equal(t, "/tmp/moox-storage", storageRoot(""))
	assert.Equal(t, "/custom", storageRoot("/custom"))
}

type stubMetadataStore struct {
	spaces    map[string]*pb.Space
	devices   []*pb.Device
	lastSpace *pb.Space
	lastNode  *pb.PrimaryStoreNode
}

func (s *stubMetadataStore) UpsertSpace(_ context.Context, space *pb.Space) (*pb.Space, error) {
	if s.spaces == nil {
		s.spaces = map[string]*pb.Space{}
	}
	copied := proto.Clone(space).(*pb.Space)
	s.spaces[copied.GetSpaceId()] = copied
	s.lastSpace = copied
	return copied, nil
}

func (s *stubMetadataStore) GetSpace(_ context.Context, spaceID string) (*pb.Space, error) {
	if space, ok := s.spaces[spaceID]; ok {
		return proto.Clone(space).(*pb.Space), nil
	}
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) Close() error { return nil }

func (s *stubMetadataStore) InitSchema(context.Context) error { return nil }

func (s *stubMetadataStore) TableNames(context.Context) ([]string, error) { return nil, nil }

func (s *stubMetadataStore) ListSpaces(context.Context, string, *pb.Page) ([]*pb.Space, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) GetView(context.Context, string, string) (*pb.View, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListViews(context.Context, string, string, string, *pb.Page) ([]*pb.View, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) ListViewsByDataset(context.Context, string, string) ([]*pb.View, error) {
	return nil, nil
}

func (s *stubMetadataStore) ListViewColumns(context.Context, string, string, *pb.Page) ([]*pb.ViewColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertView(context.Context, *pb.View) (*pb.View, error) { return nil, nil }

func (s *stubMetadataStore) UpsertViewColumn(context.Context, *pb.ViewColumn) (*pb.ViewColumn, error) {
	return nil, nil
}

func (s *stubMetadataStore) ClaimViewIndexBuild(context.Context, *pb.ClaimViewIndexBuildReq) (*pb.ViewIndexBuild, bool, error) {
	return nil, false, nil
}

func (s *stubMetadataStore) UpdateViewIndexBuild(context.Context, *pb.UpdateViewIndexBuildReq) (*pb.ViewIndexBuild, error) {
	return nil, nil
}

func (s *stubMetadataStore) ActivateViewIndex(context.Context, *pb.ActivateViewIndexReq) (*pb.View, error) {
	return nil, nil
}

func (s *stubMetadataStore) FailViewIndexBuild(context.Context, *pb.FailViewIndexBuildReq) (*pb.ViewIndexBuild, error) {
	return nil, nil
}

func (s *stubMetadataStore) GetDataSource(context.Context, string, string) (*pb.DataSource, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListDataSources(context.Context, string, string, string, *pb.Page) ([]*pb.DataSource, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertDataSource(context.Context, *pb.DataSource) (*pb.DataSource, error) {
	return nil, nil
}

func (s *stubMetadataStore) GetSubject(context.Context, string, string) (*pb.Subject, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListSubjects(context.Context, string, string, string, []string, *pb.Page) ([]*pb.Subject, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) ListSubjectSymbols(context.Context, string, string, string, string, *pb.Page) ([]*pb.SubjectSymbol, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertSubject(context.Context, *pb.Subject) (*pb.Subject, error) { return nil, nil }

func (s *stubMetadataStore) UpsertSubjectSymbol(context.Context, *pb.SubjectSymbol) (*pb.SubjectSymbol, error) {
	return nil, nil
}

func (s *stubMetadataStore) RegisterDataSubject(context.Context, *pb.Subject, *pb.SubjectSymbol, []*pb.DatasetSubject) (*pb.Subject, []*pb.DatasetSubject, error) {
	return nil, nil, nil
}

func (s *stubMetadataStore) GetDataset(context.Context, string, string) (*pb.Dataset, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListDatasets(context.Context, string, string, pb.DataKind, string, *pb.Page) ([]*pb.Dataset, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) ListDatasetSubjects(context.Context, string, string, string, *pb.Page) ([]*pb.DatasetSubject, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertDataset(context.Context, *pb.Dataset) (*pb.Dataset, error) { return nil, nil }

func (s *stubMetadataStore) BindDatasetSubject(context.Context, *pb.DatasetSubject) (*pb.DatasetSubject, error) {
	return nil, nil
}

func (s *stubMetadataStore) GetField(context.Context, string, string) (*pb.Field, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListFields(context.Context, string, pb.FieldValueType, *pb.Page) ([]*pb.Field, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertField(context.Context, *pb.Field) (*pb.Field, error) { return nil, nil }

func (s *stubMetadataStore) GetFactor(context.Context, string, string) (*pb.Factor, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListFactors(context.Context, string, string, *pb.Page) ([]*pb.Factor, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertFactor(context.Context, *pb.Factor) (*pb.Factor, error) { return nil, nil }

func (s *stubMetadataStore) ListDatasetColumns(context.Context, string, string, *pb.Page) ([]*pb.DatasetColumn, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertDatasetColumn(context.Context, *pb.DatasetColumn) (*pb.DatasetColumn, error) {
	return nil, nil
}

func (s *stubMetadataStore) GetPrimaryStoreNode(context.Context, string) (*pb.PrimaryStoreNode, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListPrimaryStoreNodes(context.Context, *pb.Page) ([]*pb.PrimaryStoreNode, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertPrimaryStoreNode(_ context.Context, item *pb.PrimaryStoreNode) (*pb.PrimaryStoreNode, error) {
	copied := proto.Clone(item).(*pb.PrimaryStoreNode)
	s.lastNode = copied
	return copied, nil
}

func (s *stubMetadataStore) GetDevice(_ context.Context, deviceID string) (*pb.Device, error) {
	for _, device := range s.devices {
		if device.GetDeviceId() == deviceID {
			return proto.Clone(device).(*pb.Device), nil
		}
	}
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListDevices(_ context.Context, nodeID string, _ string, _ *pb.Page) ([]*pb.Device, *pb.PageResult, error) {
	var out []*pb.Device
	for _, device := range s.devices {
		if nodeID == "" || device.GetNodeId() == nodeID {
			out = append(out, proto.Clone(device).(*pb.Device))
		}
	}
	return out, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertDevice(_ context.Context, item *pb.Device) (*pb.Device, error) {
	copied := proto.Clone(item).(*pb.Device)
	s.devices = append(s.devices, copied)
	return copied, nil
}

func (s *stubMetadataStore) GetPrimaryStoreRoute(context.Context, string, string) (*pb.PrimaryStoreRoute, error) {
	return nil, os.ErrNotExist
}

func (s *stubMetadataStore) ListPrimaryStoreRoutes(context.Context, string, string, string, string, *pb.Page) ([]*pb.PrimaryStoreRoute, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) UpsertPrimaryStoreRoute(context.Context, *pb.PrimaryStoreRoute) (*pb.PrimaryStoreRoute, error) {
	return nil, nil
}

func (s *stubMetadataStore) ListArchiveFiles(context.Context, string, string, *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error) {
	return nil, &pb.PageResult{}, nil
}

func (s *stubMetadataStore) RegisterArchiveFile(context.Context, *pb.ArchiveFile) (*pb.ArchiveFile, error) {
	return nil, nil
}

var _ metadata.Store = (*stubMetadataStore)(nil)
