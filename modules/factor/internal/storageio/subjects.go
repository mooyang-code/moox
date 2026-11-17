package storageio

import (
	"context"
	"errors"
	"fmt"
	"sort"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

const subjectPageSize = 1000

// ListDatasetSubjects returns the sorted active subjects of a dataset.
func (c *Client) ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error) {
	return c.listDatasetSubjects(ctx, spaceID, datasetID, true)
}

// ListAllDatasetSubjects 返回数据集登记过的全部标的（含已下架、停用的），排序：试算 observed 成员的历史截面要用到
// 本期不在册、但历史上有数据的标的。
func (c *Client) ListAllDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error) {
	return c.listDatasetSubjects(ctx, spaceID, datasetID, false)
}

func (c *Client) listDatasetSubjects(ctx context.Context, spaceID, datasetID string, activeOnly bool) ([]string, error) {
	if err := c.metadataReady("list dataset subjects"); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	for page := uint32(1); ; page++ {
		rsp, err := c.metadata.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{
			SpaceId: spaceID, DatasetId: datasetID,
			Page: &commonpb.Page{Page: page, Size: subjectPageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("%w: list dataset subjects: %v", ErrInfra, err)
		}
		if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("list dataset subjects failed: %s", rsp.GetRetInfo().GetMsg())
		}
		for _, item := range rsp.GetDatasetSubjects() {
			if item != nil && item.GetSubjectId() != "" && (!activeOnly || item.GetStatus() == "active") {
				seen[item.GetSubjectId()] = struct{}{}
			}
		}
		result := rsp.GetPageResult()
		if result == nil || !result.GetHasMore() || len(rsp.GetDatasetSubjects()) == 0 {
			break
		}
		if page >= 100000 {
			return nil, errors.New("dataset subject pagination exceeded the page limit")
		}
	}
	subjects := make([]string, 0, len(seen))
	for subject := range seen {
		subjects = append(subjects, subject)
	}
	sort.Strings(subjects)
	return subjects, nil
}
