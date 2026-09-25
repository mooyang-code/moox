// Package storagesource loads planner inputs from MooX storage metadata.
package storagesource

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/transport"
)

const storagePageSize = 500

const (
	metadataPrimaryTimeout  = 6 * time.Second
	metadataFallbackTimeout = 8 * time.Second
)

type metadataClient interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq, ...client.Option) (*storagepb.GetDatasetRsp, error)
	ResolveSubjects(context.Context, *storagepb.ResolveSubjectsReq, ...client.Option) (*storagepb.ResolveSubjectsRsp, error)
}

type datasetColumnClient interface {
	ListDatasetColumns(context.Context, *storagepb.ListDatasetColumnsReq, ...client.Option) (*storagepb.ListDatasetColumnsRsp, error)
}

// DatasetInfo is the minimal Dataset contract required by Collector rules.
type DatasetInfo struct {
	DataSourceID string
	DataNodeID   string
	DataKind     storagepb.DataKind
	Status       string
	Freqs        []string
	SubjectTags  []string
	Columns      []string
	ColumnTypes  map[string]storagepb.FieldValueType
	Attributes   map[string]string
	KeepDuration string
	Revision     uint64
}

// DatasetSource loads Dataset metadata and resolves the active Subject union
// described by Dataset.SubjectTags. Provider wire symbols are deliberately not
// persisted; callers resolve them from the canonical SubjectID.
type DatasetSource struct {
	metadata metadataClient
}

func (s *DatasetSource) GetDataset(ctx context.Context, spaceID, datasetID string) (DatasetInfo, error) {
	if s == nil || s.metadata == nil {
		return DatasetInfo{}, fmt.Errorf("dataset source is not initialized")
	}
	spaceID, datasetID = strings.TrimSpace(spaceID), strings.TrimSpace(datasetID)
	if datasetID == "" {
		return DatasetInfo{}, fmt.Errorf("dataset_id is required")
	}
	rsp, err := s.metadata.GetDataset(ctx, &storagepb.GetDatasetReq{SpaceId: spaceID, DatasetId: datasetID})
	if err != nil {
		return DatasetInfo{}, fmt.Errorf("get dataset: %w", err)
	}
	if err := ensureStorageOK("get dataset", rsp.GetRetInfo()); err != nil {
		return DatasetInfo{}, err
	}
	dataset := rsp.GetDataset()
	if dataset == nil {
		return DatasetInfo{}, fmt.Errorf("get dataset: empty dataset")
	}
	info := DatasetInfo{
		DataSourceID: strings.TrimSpace(dataset.GetDataSourceId()),
		DataNodeID:   strings.TrimSpace(dataset.GetDataNodeId()),
		DataKind:     dataset.GetDataKind(),
		Status:       strings.ToLower(strings.TrimSpace(dataset.GetStatus())),
		Freqs:        append([]string(nil), dataset.GetFreqs()...),
		SubjectTags:  append([]string(nil), dataset.GetSubjectTags()...),
		Attributes:   cloneAttributes(dataset.GetAttributes()),
		KeepDuration: strings.TrimSpace(dataset.GetKeepDuration()),
		Revision:     dataset.GetRevision(),
		ColumnTypes:  make(map[string]storagepb.FieldValueType),
	}
	if columns, ok := s.metadata.(datasetColumnClient); ok {
		for page := uint32(1); page <= 100; page++ {
			columnsRsp, callErr := columns.ListDatasetColumns(ctx, &storagepb.ListDatasetColumnsReq{SpaceId: spaceID, DatasetId: datasetID, Page: &storagepb.Page{Page: page, Size: storagePageSize}})
			if callErr != nil {
				return DatasetInfo{}, fmt.Errorf("list dataset columns: %w", callErr)
			}
			if err := ensureStorageOK("list dataset columns", columnsRsp.GetRetInfo()); err != nil {
				return DatasetInfo{}, err
			}
			for _, column := range columnsRsp.GetColumns() {
				if column == nil || !strings.EqualFold(strings.TrimSpace(column.GetStatus()), "active") || strings.TrimSpace(column.GetColumnName()) == "" {
					continue
				}
				name := strings.TrimSpace(column.GetColumnName())
				info.Columns = append(info.Columns, name)
				info.ColumnTypes[name] = column.GetValueType()
			}
			if columnsRsp.GetPageResult() == nil || !columnsRsp.GetPageResult().GetHasMore() || len(columnsRsp.GetColumns()) == 0 {
				break
			}
		}
	}
	return info, nil
}

func cloneAttributes(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

// ResolveSubjects returns active members of the selected tags. Storage owns
// the union semantics and therefore remains the source of truth for this
// method.
func (s *DatasetSource) ResolveSubjects(ctx context.Context, spaceID string, tagIDs []string) ([]domain.Subject, error) {
	if s == nil || s.metadata == nil {
		return nil, fmt.Errorf("dataset source is not initialized")
	}
	rsp, err := s.metadata.ResolveSubjects(ctx, &storagepb.ResolveSubjectsReq{SpaceId: strings.TrimSpace(spaceID), TagIds: append([]string(nil), tagIDs...)})
	if err != nil {
		return nil, fmt.Errorf("resolve subjects: %w", err)
	}
	if err := ensureStorageOK("resolve subjects", rsp.GetRetInfo()); err != nil {
		return nil, err
	}
	items := make([]domain.Subject, 0, len(rsp.GetSubjects()))
	for _, subject := range rsp.GetSubjects() {
		if subject == nil || strings.TrimSpace(subject.GetSubjectId()) == "" {
			continue
		}
		items = append(items, domain.Subject{SubjectID: strings.TrimSpace(subject.GetSubjectId()), Name: strings.TrimSpace(subject.GetName()), Status: strings.TrimSpace(subject.GetStatus())})
	}
	return items, nil
}

// ListSubjects is retained as a compatibility adapter for the resample
// planner. It derives the list from the Dataset's tags and never reads a
// source-side symbol mapping.
func (s *DatasetSource) ListSubjects(ctx context.Context, spaceID, datasetID, _ string) ([]domain.DatasetSubject, error) {
	dataset, err := s.GetDataset(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	items, err := s.ResolveSubjects(ctx, spaceID, dataset.SubjectTags)
	if err != nil {
		return nil, err
	}
	result := make([]domain.DatasetSubject, 0, len(items))
	for _, item := range items {
		result = append(result, domain.DatasetSubject{SubjectID: item.SubjectID, SubjectName: item.Name, Status: item.Status})
	}
	return result, nil
}

func (s *DatasetSource) ListResampleSubjects(ctx context.Context, spaceID, datasetID string) ([]domain.DatasetSubject, error) {
	return s.ListSubjects(ctx, spaceID, datasetID, "")
}

func (s *DatasetSource) ListResampleSubjectsForTask(ctx context.Context, spaceID, datasetID, _, _ string) ([]domain.DatasetSubject, error) {
	return s.ListSubjects(ctx, spaceID, datasetID, "")
}

// metadataFailoverClient keeps the authenticated gateway path first and uses
// the private Metadata listener only for transient read failures.
type metadataFailoverClient struct {
	primary   metadataClient
	secondary metadataClient
}

func (c *metadataFailoverClient) GetDataset(ctx context.Context, req *storagepb.GetDatasetReq, opts ...client.Option) (*storagepb.GetDatasetRsp, error) {
	primaryCtx, cancel := context.WithTimeout(ctx, metadataPrimaryTimeout)
	rsp, err := c.primary.GetDataset(primaryCtx, req, opts...)
	cancel()
	if err == nil || c.secondary == nil {
		return rsp, err
	}
	fallbackCtx, fallbackCancel := context.WithTimeout(context.WithoutCancel(ctx), metadataFallbackTimeout)
	defer fallbackCancel()
	secondaryRsp, secondaryErr := c.secondary.GetDataset(fallbackCtx, req)
	if secondaryErr == nil {
		log.WarnContextf(ctx, "storage metadata gateway failed, used direct Metadata fallback action=get_dataset error=%v", err)
		return secondaryRsp, nil
	}
	return nil, fmt.Errorf("gateway: %w; direct metadata: %v", err, secondaryErr)
}

func (c *metadataFailoverClient) ResolveSubjects(ctx context.Context, req *storagepb.ResolveSubjectsReq, opts ...client.Option) (*storagepb.ResolveSubjectsRsp, error) {
	primaryCtx, cancel := context.WithTimeout(ctx, metadataPrimaryTimeout)
	rsp, err := c.primary.ResolveSubjects(primaryCtx, req, opts...)
	cancel()
	if err == nil || c.secondary == nil {
		return rsp, err
	}
	fallbackCtx, fallbackCancel := context.WithTimeout(context.WithoutCancel(ctx), metadataFallbackTimeout)
	defer fallbackCancel()
	secondaryRsp, secondaryErr := c.secondary.ResolveSubjects(fallbackCtx, req)
	if secondaryErr == nil {
		log.WarnContextf(ctx, "storage metadata gateway failed, used direct Metadata fallback action=resolve_subjects error=%v", err)
		return secondaryRsp, nil
	}
	return nil, fmt.Errorf("gateway: %w; direct metadata: %v", err, secondaryErr)
}

func (c *metadataFailoverClient) ListDatasetColumns(ctx context.Context, req *storagepb.ListDatasetColumnsReq, opts ...client.Option) (*storagepb.ListDatasetColumnsRsp, error) {
	primary, ok := c.primary.(datasetColumnClient)
	if !ok {
		return nil, fmt.Errorf("list dataset columns: primary metadata client does not support columns")
	}
	primaryCtx, cancel := context.WithTimeout(ctx, metadataPrimaryTimeout)
	rsp, err := primary.ListDatasetColumns(primaryCtx, req, opts...)
	cancel()
	if err == nil || c.secondary == nil {
		return rsp, err
	}
	secondary, ok := c.secondary.(datasetColumnClient)
	if !ok {
		return nil, err
	}
	fallbackCtx, fallbackCancel := context.WithTimeout(context.WithoutCancel(ctx), metadataFallbackTimeout)
	defer fallbackCancel()
	secondaryRsp, secondaryErr := secondary.ListDatasetColumns(fallbackCtx, req)
	if secondaryErr == nil {
		log.WarnContextf(ctx, "storage metadata gateway failed, used direct Metadata fallback action=list_dataset_columns error=%v", err)
		return secondaryRsp, nil
	}
	return nil, fmt.Errorf("gateway: %w; direct metadata: %v", err, secondaryErr)
}

func NewDatasetSource(metadataTarget string) *DatasetSource {
	primaryTarget := normalizeTRPCTarget(metadataTarget, "11003")
	primary := storagepb.NewMetadataClientProxy(append(gatewayauth.NewTRPCClientOptions(primaryTarget, storageGatewayNodeID(), gatewayauth.CredentialsFromEnv()), client.WithTransport(transport.DefaultClientTransport))...)
	secondaryTarget := directMetadataTarget(primaryTarget)
	if secondaryTarget == "" {
		return &DatasetSource{metadata: primary}
	}
	secondary := storagepb.NewMetadataClientProxy(client.WithTarget(secondaryTarget), client.WithNetwork("tcp"), client.WithProtocol("trpc"), client.WithTransport(transport.DefaultClientTransport))
	return &DatasetSource{metadata: &metadataFailoverClient{primary: primary, secondary: secondary}}
}

func directMetadataTarget(raw string) string {
	if override := strings.TrimSpace(os.Getenv("MOOX_COLLECTOR_STORAGE_METADATA_TARGET")); override != "" {
		return normalizeTRPCTarget(override, "20100")
	}
	parsed, err := url.Parse(normalizeTRPCTarget(raw, "11003"))
	if err != nil || parsed.Scheme != "ip" || parsed.Hostname() == "" || parsed.Port() != "11003" {
		return ""
	}
	return "ip://" + net.JoinHostPort(parsed.Hostname(), "20100")
}

func storageGatewayNodeID() string {
	if nodeID := strings.TrimSpace(os.Getenv("MOOX_COLLECTOR_STORAGE_RPC_GATEWAY_NODE_ID")); nodeID != "" {
		return nodeID
	}
	return gatewayauth.ServiceGatewayNodeID()
}

func normalizeTRPCTarget(raw, defaultPort string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "ip://127.0.0.1:" + defaultPort
	}
	if strings.HasPrefix(raw, "ip://") || strings.Contains(raw, "://") {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Host != "" {
		return "ip://" + parsed.Host
	}
	if strings.Contains(raw, ":") {
		return "ip://" + raw
	}
	return raw
}

func ensureStorageOK(action string, ret *storagepb.RetInfo) error {
	if ret == nil {
		return fmt.Errorf("%s: empty ret_info", action)
	}
	if ret.GetCode() != storagepb.ErrorCode_SUCCESS {
		return fmt.Errorf("%s: %s", action, ret.GetMsg())
	}
	return nil
}
