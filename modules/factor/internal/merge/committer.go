package merge

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/client"
)

type primaryCommitClient interface {
	CommitInput(context.Context, *storagepb.PrimaryCommitInputReq, ...client.Option) (*storagepb.PrimaryCommitInputRsp, error)
}

// StorageCommitter submits complete base rows through the authorized CommitInput RPC.
type StorageCommitter struct {
	client         primaryCommitClient
	auth           *commonpb.AuthInfo
	spaceID        string
	requiredFields []string
}

func NewStorageCommitter(client primaryCommitClient, auth *commonpb.AuthInfo, spaceID string, requiredFields []string) *StorageCommitter {
	fields := append([]string(nil), requiredFields...)
	sort.Strings(fields)
	return &StorageCommitter{client: client, auth: auth, spaceID: spaceID, requiredFields: fields}
}

func (c *StorageCommitter) CommitInput(ctx context.Context, commitID string, key RowKey, fields map[string]float64, ready bool) error {
	if c == nil || c.client == nil {
		return fmt.Errorf("merge storage committer is not initialized")
	}
	if !ready {
		return fmt.Errorf("merge must not commit incomplete input")
	}
	row := &storagepb.RowFieldUpsert{
		Key: &storagepb.RowKey{
			SpaceId: c.spaceID, DatasetId: key.DatasetID,
			Kind: &storagepb.RowKey_TimeSeries{TimeSeries: &storagepb.TimeSeriesRowKey{
				SubjectId: key.SubjectID, Freq: key.Frequency,
				DataTime: key.PeriodTime.UTC().Format(time.RFC3339Nano), SeriesTag: key.SeriesTag,
			}},
		},
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		row.Fields = append(row.Fields, &storagepb.FieldValue{
			FieldId: name,
			Value:   &storagepb.TypedValue{Value: &storagepb.TypedValue_DoubleValue{DoubleValue: fields[name]}},
		})
	}
	rsp, err := c.client.CommitInput(ctx, &storagepb.PrimaryCommitInputReq{
		AuthInfo: c.auth, CommitId: commitID, RequiredFields: c.requiredFields, Row: row,
	})
	if err != nil {
		return fmt.Errorf("commit mdataset input: %w", err)
	}
	if rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		msg := strings.TrimSpace(rsp.GetRetInfo().GetMsg())
		if msg == "" {
			msg = rsp.GetRetInfo().GetCode().String()
		}
		return fmt.Errorf("commit mdataset input: %s", msg)
	}
	return nil
}
