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
	WriteFactorRows(context.Context, *storagepb.PrimaryWriteFactorRowsReq, ...client.Option) (*storagepb.PrimaryWriteFactorRowsRsp, error)
	ReportFactorPeriodComputed(context.Context, *storagepb.ReportFactorPeriodComputedReq, ...client.Option) (*storagepb.ReportFactorPeriodComputedRsp, error)
	GetFactorPeriodComputed(context.Context, *storagepb.GetFactorPeriodComputedReq, ...client.Option) (*storagepb.GetFactorPeriodComputedRsp, error)
	DeleteDatasetRows(context.Context, *storagepb.PrimaryDeleteDatasetRowsReq, ...client.Option) (*storagepb.PrimaryDeleteDatasetRowsRsp, error)
	RestoreDatasetRows(context.Context, *storagepb.PrimaryRestoreDatasetRowsReq, ...client.Option) (*storagepb.PrimaryRestoreDatasetRowsRsp, error)
}

type MetadataClient interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	ListDatasetColumns(context.Context, *storagepb.ListDatasetColumnsReq, ...client.Option) (*storagepb.ListDatasetColumnsRsp, error)
	CreateDataset(context.Context, *storagepb.CreateDatasetReq, ...client.Option) (*storagepb.CreateDatasetRsp, error)
	UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq, ...client.Option) (*storagepb.UpsertDatasetColumnRsp, error)
	ActivateDataset(context.Context, *storagepb.ActivateDatasetReq, ...client.Option) (*storagepb.ActivateDatasetRsp, error)
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq, ...client.Option) (*storagepb.DeleteDatasetRsp, error)
	UpdateDataset(context.Context, *storagepb.UpdateDatasetReq, ...client.Option) (*storagepb.UpdateDatasetRsp, error)
	ListDatasetSubjects(context.Context, *storagepb.ListDatasetSubjectsReq, ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error)
}

type Client struct {
	primary  PrimaryStoreClient
	metadata MetadataClient
	auth     *commonpb.AuthInfo
}

func NewClient(primary PrimaryStoreClient, metadata MetadataClient, auth *commonpb.AuthInfo) *Client {
	return &Client{primary: primary, metadata: metadata, auth: auth}
}

// NewClientWithOptions 创建经给定 tRPC 客户端选项访问 Storage 的客户端：FactorMgr 传入 gatewayclient 的选项。
func NewClientWithOptions(options []client.Option, auth *commonpb.AuthInfo) *Client {
	return NewClient(storagepb.NewPrimaryStoreClientProxy(options...), storagepb.NewMetadataClientProxy(options...), auth)
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

type FactorState struct {
	FactorID       string
	Status         string
	FailedSubjects []string
	SourceHash     string
	DefinitionHash string
}

type Store interface {
	DatasetColumns(ctx context.Context, spaceID, datasetID string) ([]string, error)
	ReadWindow(ctx context.Context, req ReadRequest) (map[string]*Frame, error)
	WriteRows(ctx context.Context, spaceID, datasetID, commitID string, rows []ResultRow) error
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
