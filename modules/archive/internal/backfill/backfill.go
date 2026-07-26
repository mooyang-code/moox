package backfill

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/archive/internal/domain"
	"github.com/mooyang-code/moox/modules/archive/internal/journal"
	"github.com/mooyang-code/moox/modules/archive/internal/writer"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/trpcretry"
	"trpc.group/trpc-go/trpc-go/client"
)

type AccessClient interface {
	ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
}
type MetadataClient interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	ListDatasetSubjects(context.Context, *storagepb.ListDatasetSubjectsReq, ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error)
}
type Plan struct {
	SpaceID   string
	DatasetID string
	SubjectID string
	Freq      string
	Start     string
	End       string
	Confirm   bool
}

func (p Plan) Partitions() []string {
	start, _ := time.Parse(time.RFC3339, p.Start)
	end, _ := time.Parse(time.RFC3339, p.End)
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return nil
	}
	out := []string{}
	cursor := time.Date(start.UTC().Year(), start.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for !cursor.After(end.UTC()) {
		out = append(out, cursor.Format("200601"))
		cursor = cursor.AddDate(0, 1, 0)
	}
	return out
}

type Backfiller struct {
	access   AccessClient
	metadata MetadataClient
	journal  *journal.Store
	writer   *writer.Writer
}

func New(access AccessClient, metadata MetadataClient, store *journal.Store, w *writer.Writer) *Backfiller {
	return &Backfiller{access: access, metadata: metadata, journal: store, writer: w}
}
func (b *Backfiller) Run(ctx context.Context, plan Plan) (int, error) {
	if !plan.Confirm {
		return 0, fmt.Errorf("backfill requires --confirm")
	}
	start, err := time.Parse(time.RFC3339Nano, plan.Start)
	if err != nil {
		return 0, err
	}
	end, err := time.Parse(time.RFC3339Nano, plan.End)
	if err != nil {
		return 0, err
	}
	key := &storagepb.TimeSeriesKey{SpaceId: plan.SpaceID, DatasetId: plan.DatasetID, SubjectId: plan.SubjectID, Freq: plan.Freq}
	page := uint32(1)
	total := 0
	for {
		rsp, err := b.access.ReadTimeSeriesRows(ctx, &storagepb.ReadTimeSeriesRowsReq{Keys: []*storagepb.TimeSeriesKey{key}, TimeRange: &storagepb.TimeRange{StartTime: start.UTC().Format(time.RFC3339Nano), EndTime: end.UTC().Format(time.RFC3339Nano)}, Order: storagepb.SortOrder_SORT_ORDER_ASC, Page: &commonpb.Page{Page: page, Size: 500}}, client.WithFilter(trpcretry.ReadOnly()))
		if err != nil {
			return total, err
		}
		if rsp == nil || rsp.GetRetInfo() == nil || rsp.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
			return total, fmt.Errorf("storage read failed")
		}
		patches, err := rowsToPatches(rsp.GetRows(), time.Now().UTC())
		if err != nil {
			return total, err
		}
		if len(patches) > 0 {
			_, err = b.journal.Append(ctx, domain.EventBatch{MessageID: fmt.Sprintf("backfill/%s/%s/%s/%s/%d", plan.SpaceID, plan.DatasetID, plan.SubjectID, plan.Freq, page), Rows: patches})
			if err != nil {
				return total, err
			}
			total += len(patches)
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			break
		}
		page++
	}
	if err := b.writer.WriteDirty(ctx, 100000); err != nil {
		return total, err
	}
	return total, nil
}
func rowsToPatches(rows []*storagepb.TimeSeriesRow, writtenAt time.Time) ([]domain.RowPatch, error) {
	out := make([]domain.RowPatch, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.GetKey() == nil {
			return nil, fmt.Errorf("storage row key is required")
		}
		key := row.GetKey()
		t, err := time.Parse(time.RFC3339Nano, key.GetDataTime())
		if err != nil {
			return nil, err
		}
		dims, err := domain.CanonicalStringMap(nil)
		if err != nil {
			return nil, err
		}
		columns := map[string]domain.Scalar{}
		for _, field := range row.GetFields() {
			if _, exists := columns[field.GetFieldId()]; exists {
				return nil, fmt.Errorf("duplicate column")
			}
			scalar, err := domain.ScalarFromField(field.GetFieldId(), field.GetValue())
			if err != nil {
				return nil, err
			}
			columns[field.GetFieldId()] = scalar
		}
		attrs := map[string]string{}
		for k, v := range row.GetAttributes() {
			attrs[k] = v
		}
		out = append(out, domain.RowPatch{Partition: domain.PartitionKey{SpaceID: key.GetSpaceId(), DatasetID: key.GetDatasetId(), SubjectID: key.GetSubjectId(), Freq: key.GetFreq(), Month: domain.MonthOf(t)}, DataTime: t.UTC(), DimensionsJSON: dims, Attributes: attrs, WrittenAt: writtenAt, Columns: columns})
	}
	return out, nil
}

func NormalizeTarget(raw string, defaultPort string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "ip://127.0.0.1:" + defaultPort
	}
	if strings.Contains(raw, "://") {
		return raw
	}
	return "ip://" + raw
}
