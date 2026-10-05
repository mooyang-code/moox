package storageio

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	mooxsecurity "github.com/mooyang-code/moox/packages/security"
)

// AppID is the Storage identity of the factor module. moox-factor-mgr and
// moox-factor-engine both write factor results under it.
const AppID = "moox-factor"

// AuthInfo signs the factor AppID with MOOX_STORAGE_PRIMARY_AUTH_SECRET when
// the secret is configured.
func AuthInfo(requestID string) *commonpb.AuthInfo {
	return NewAuthInfo(requestID, os.Getenv("MOOX_STORAGE_PRIMARY_AUTH_SECRET"))
}

// NewAuthInfo signs the factor AppID with an explicit Storage secret.
func NewAuthInfo(requestID, secret string) *commonpb.AuthInfo {
	auth := &commonpb.AuthInfo{AppId: AppID, Operator: AppID, RequestId: requestID}
	if secret = strings.TrimSpace(secret); secret != "" {
		auth.AppKey = mooxsecurity.HMACSHA256Hex(secret, []byte(auth.AppId))
	}
	return auth
}

const subjectPageSize = 1000

// ListDatasetSubjects returns the sorted active subjects of a dataset.
func (c *Client) ListDatasetSubjects(ctx context.Context, spaceID, datasetID string) ([]string, error) {
	if err := c.metadataReady("list dataset subjects"); err != nil {
		return nil, err
	}
	seen := make(map[string]struct{})
	for page := uint32(1); ; page++ {
		rsp, err := c.metadata.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{
			AuthInfo: c.auth, SpaceId: spaceID, DatasetId: datasetID,
			Page: &commonpb.Page{Page: page, Size: subjectPageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("%w: list dataset subjects: %v", ErrInfra, err)
		}
		if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			return nil, fmt.Errorf("list dataset subjects failed: %s", rsp.GetRetInfo().GetMsg())
		}
		for _, item := range rsp.GetDatasetSubjects() {
			if item != nil && item.GetSubjectId() != "" && item.GetStatus() == "active" {
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
