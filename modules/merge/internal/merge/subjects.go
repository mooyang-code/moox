package merge

import (
	"context"
	"fmt"
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/client"
)

const datasetSubjectPageSize = 1000

type datasetSubjectClient interface {
	ListDatasetSubjects(context.Context, *storagepb.ListDatasetSubjectsReq, ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error)
}

// MetadataSubjectLister loads activated Dataset memberships for non-timeseries merge freeze.
type MetadataSubjectLister struct {
	client datasetSubjectClient
	auth   *commonpb.AuthInfo
}

func NewMetadataSubjectLister(client datasetSubjectClient, auth *commonpb.AuthInfo) *MetadataSubjectLister {
	return &MetadataSubjectLister{client: client, auth: auth}
}

func (l *MetadataSubjectLister) ListActiveDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error) {
	if l == nil || l.client == nil {
		return nil, fmt.Errorf("dataset subject lister is not initialized")
	}
	spaceID = strings.TrimSpace(spaceID)
	datasetID = strings.TrimSpace(datasetID)
	if spaceID == "" || datasetID == "" {
		return nil, fmt.Errorf("space_id and dataset_id are required")
	}
	var subjects []string
	for page := uint32(1); page <= 10000; page++ {
		rsp, err := l.client.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{
			AuthInfo: l.auth, SpaceId: spaceID, DatasetId: datasetID,
			Page: &commonpb.Page{Page: page, Size: datasetSubjectPageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("list dataset subjects: %w", err)
		}
		if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			msg := strings.TrimSpace(rsp.GetRetInfo().GetMsg())
			if msg == "" {
				msg = rsp.GetRetInfo().GetCode().String()
			}
			return nil, fmt.Errorf("list dataset subjects: %s", msg)
		}
		for _, item := range rsp.GetDatasetSubjects() {
			if !activeDatasetSubject(item) {
				continue
			}
			subjects = append(subjects, item.GetSubjectId())
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetDatasetSubjects()) == 0 {
			return canonicalizeUniverseSubjects(subjects), nil
		}
	}
	return nil, fmt.Errorf("too many dataset subject pages for %s/%s", spaceID, datasetID)
}

func activeDatasetSubject(subject *storagepb.DatasetSubject) bool {
	if subject == nil || strings.TrimSpace(subject.GetSubjectId()) == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(subject.GetStatus())) {
	case "", "active":
		return true
	default:
		return false
	}
}
