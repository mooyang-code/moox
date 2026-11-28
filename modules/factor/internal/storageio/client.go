package storageio

import (
	"context"
	"errors"
	"fmt"
	"time"

	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/errs"
)

var ErrInfra = errors.New("storage infrastructure failure")

type PrimaryStoreClient interface {
	ReadTimeSeriesRows(context.Context, *storagepb.ReadTimeSeriesRowsReq, ...client.Option) (*storagepb.ReadTimeSeriesRowsRsp, error)
	ReadDatasetChanges(context.Context, *storagepb.ReadDatasetChangesReq, ...client.Option) (*storagepb.ReadDatasetChangesRsp, error)
	UpsertFields(context.Context, *storagepb.PrimaryUpsertFieldsReq, ...client.Option) (*storagepb.PrimaryUpsertFieldsRsp, error)
	ReportDatasetPeriodCompleted(context.Context, *storagepb.ReportDatasetPeriodCompletedReq, ...client.Option) (*storagepb.ReportDatasetPeriodCompletedRsp, error)
	GetDatasetPeriodCompleted(context.Context, *storagepb.GetDatasetPeriodCompletedReq, ...client.Option) (*storagepb.GetDatasetPeriodCompletedRsp, error)
}

type MetadataClient interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	ListDatasetColumns(context.Context, *storagepb.ListDatasetColumnsReq, ...client.Option) (*storagepb.ListDatasetColumnsRsp, error)
	UpsertDataset(context.Context, *storagepb.UpsertDatasetReq, ...client.Option) (*storagepb.UpsertDatasetRsp, error)
	UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq, ...client.Option) (*storagepb.UpsertDatasetColumnRsp, error)
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq, ...client.Option) (*storagepb.DeleteDatasetRsp, error)
	ListDatasetSubjects(context.Context, *storagepb.ListDatasetSubjectsReq, ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error)
}

type Client struct {
	primary  PrimaryStoreClient
	metadata MetadataClient
}

func NewClient(primary PrimaryStoreClient, metadata MetadataClient) *Client {
	return &Client{primary: primary, metadata: metadata}
}

type Frame struct {
	SubjectID string
	Columns   []string
	Rows      [][]any
}

type ResultRow struct {
	SubjectID string
	DataTime  time.Time
	SeriesTag string
	Fields    map[string]any
}

type PeriodMarker struct {
	SpaceID          string
	ResultDatasetID  string
	SourceDatasetID  string
	Frequency        string
	PeriodTime       int64
	Status           string
	UniverseSubjects []string
	FailedSubjects   []string
	Factors          []FactorState
	TriggerEventID   string
	ComputedAt       time.Time
}

// FactorState 是一个成员一期的结果状态：MemberKey 是成员键（结果数据集内唯一），FactorID 是它引用的定义；
// FailedSubjects 是该成员本期完整的失败标的（写进周期明细时才减去周期级的失败标的，见 markerToProto）。
type FactorState struct {
	MemberKey      string
	MemberKind     string
	FactorID       string
	Status         string
	FailedSubjects []string
	SourceHash     string // 填因子定义的 spec_hash；Storage 协议里这个字段仍叫 source_hash
	DefinitionHash string
}

type Store interface {
	DatasetColumns(ctx context.Context, spaceID, datasetID string) ([]string, error)
	ReadWindow(ctx context.Context, req ReadRequest) (map[string]*Frame, error)
	WriteRows(ctx context.Context, spaceID, datasetID string, rows []ResultRow) error
	ReportComputed(ctx context.Context, marker PeriodMarker) error
	ComputedExists(ctx context.Context, spaceID, datasetID, triggerEventID string, periodTime int64) (bool, error)
}

type ReadRequest struct {
	SpaceID   string
	DatasetID string
	Freq      string
	Subjects  []string
	Start     time.Time
	End       time.Time
	Columns   []string
	// PageTimeout bounds each page request separately when positive. A
	// backfill window spans many pages over a narrow link, so a total
	// deadline fails slow-but-progressing reads; a page deadline still
	// catches a stalled one.
	PageTimeout time.Duration
}

func (c *Client) primaryReady(action string) error {
	if c == nil || c.primary == nil {
		return fmt.Errorf("%s: primary storage client is unavailable", action)
	}
	return nil
}

func (c *Client) metadataReady(action string) error {
	if c == nil || c.metadata == nil {
		return fmt.Errorf("%s: metadata storage client is unavailable", action)
	}
	return nil
}

func rpcError(action string, err error) error {
	if err == nil {
		return nil
	}
	wrapped := fmt.Errorf("%s: %w", action, err)
	if errors.Is(err, context.DeadlineExceeded) ||
		errs.Code(err) == errs.RetClientTimeout || errs.Code(err) == errs.RetClientNetErr {
		return fmt.Errorf("%w: %w", ErrInfra, wrapped)
	}
	return wrapped
}

func responseError(action string, ret *commonpb.RetInfo) error {
	if ret == nil {
		return fmt.Errorf("%w: %s returned empty ret_info", ErrInfra, action)
	}
	if ret.GetCode() == commonpb.ErrorCode_SUCCESS {
		return nil
	}
	err := fmt.Errorf("%s: %s", action, ret.GetMsg())
	if ret.GetCode() == commonpb.ErrorCode_INNER_ERR {
		return fmt.Errorf("%w: %w", ErrInfra, err)
	}
	return err
}
