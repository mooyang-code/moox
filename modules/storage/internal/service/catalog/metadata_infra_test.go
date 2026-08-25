package catalog

import (
	"context"
	"fmt"
	"testing"

	"github.com/mooyang-code/moox/modules/storage/internal/service/metadata"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/stretchr/testify/require"
)

type archiveMetadataStore struct {
	metadata.Store
	items []*pb.ArchiveFile
}

func (s *archiveMetadataStore) ListArchiveFiles(ctx context.Context, spaceID string, datasetID string, page *pb.Page) ([]*pb.ArchiveFile, *pb.PageResult, error) {
	_ = ctx
	_ = spaceID
	_ = datasetID
	pageNo, pageSize := uint32(1), uint32(1000)
	if page != nil {
		if page.GetPage() > 0 {
			pageNo = page.GetPage()
		}
		if page.GetSize() > 0 {
			pageSize = page.GetSize()
		}
	}
	start := int((pageNo - 1) * pageSize)
	if start > len(s.items) {
		start = len(s.items)
	}
	end := start + int(pageSize)
	if end > len(s.items) {
		end = len(s.items)
	}
	return s.items[start:end], &pb.PageResult{Page: pageNo, Size: pageSize, Total: uint32(len(s.items)), HasMore: end < len(s.items)}, nil
}

func TestListArchiveFilesSortsBeforePagination(t *testing.T) {
	store := &archiveMetadataStore{items: []*pb.ArchiveFile{
		{ArchiveFileId: "archive-old", MinTime: "2026-01-01T00:00:00Z"},
		{ArchiveFileId: "archive-new", MinTime: "2026-02-01T00:00:00Z"},
		{ArchiveFileId: "archive-mid", MinTime: "2026-01-15T00:00:00Z"},
	}}
	svc, err := NewMetadataService(store, nil, Options{})
	require.NoError(t, err)

	rsp, err := svc.ListArchiveFiles(context.Background(), &pb.ListArchiveFilesReq{
		SortBy: "min_time", SortOrder: "desc", Page: &pb.Page{Page: 1, Size: 2},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"archive-new", "archive-mid"}, archiveFileIDs(rsp.GetArchiveFiles()))
	require.Equal(t, uint32(3), rsp.GetPageResult().GetTotal())
	require.True(t, rsp.GetPageResult().GetHasMore())
}

func TestListArchiveFilesRejectsUnsupportedSort(t *testing.T) {
	svc, err := NewMetadataService(&archiveMetadataStore{}, nil, Options{})
	require.NoError(t, err)

	for _, req := range []*pb.ListArchiveFilesReq{
		{SortBy: "file_uri"},
		{SortOrder: "desc"},
	} {
		rsp, err := svc.ListArchiveFiles(context.Background(), req)
		require.NoError(t, err)
		require.Equal(t, pb.ErrorCode_INVALID_PARAM, rsp.GetRetInfo().GetCode())
	}
}

func TestListArchiveFilesLoadsAllPagesBeforeSorting(t *testing.T) {
	items := make([]*pb.ArchiveFile, 1001)
	for i := range items {
		items[i] = &pb.ArchiveFile{ArchiveFileId: fmt.Sprintf("archive-%04d", i), MinTime: fmt.Sprintf("2026-01-%04d", i)}
	}
	items[1000].MinTime = "2027-01-01"
	svc, err := NewMetadataService(&archiveMetadataStore{items: items}, nil, Options{})
	require.NoError(t, err)

	rsp, err := svc.ListArchiveFiles(context.Background(), &pb.ListArchiveFilesReq{
		SortBy: "min_time", SortOrder: "desc", Page: &pb.Page{Page: 1, Size: 1},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"archive-1000"}, archiveFileIDs(rsp.GetArchiveFiles()))
	require.Equal(t, uint32(1001), rsp.GetPageResult().GetTotal())
}

func archiveFileIDs(items []*pb.ArchiveFile) []string {
	ids := make([]string, 0, len(items))
	for _, item := range items {
		ids = append(ids, item.GetArchiveFileId())
	}
	return ids
}
