// Package storagesource loads planner inputs from MooX storage metadata.
package storagesource

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/transport"
)

const storagePageSize = 500

type metadataClient interface {
	ListDatasetSubjects(ctx context.Context, req *storagepb.ListDatasetSubjectsReq, opts ...client.Option) (*storagepb.ListDatasetSubjectsRsp, error)
	ListSubjectSymbols(ctx context.Context, req *storagepb.ListSubjectSymbolsReq, opts ...client.Option) (*storagepb.ListSubjectSymbolsRsp, error)
}

// DatasetSource loads active dataset subjects and source-side symbols.
type DatasetSource struct {
	metadata metadataClient
}

// NewDatasetSource creates a storage metadata backed dataset source.
func NewDatasetSource(metadataTarget string) *DatasetSource {
	return &DatasetSource{
		metadata: storagepb.NewMetadataClientProxy(
			append(gatewayauth.NewTRPCClientOptions(normalizeTRPCTarget(metadataTarget, "11003"), gatewayauth.ServiceGatewayNodeID(), gatewayauth.CredentialsFromEnv()),
				client.WithTransport(transport.DefaultClientTransport))...,
		),
	}
}

func newDatasetSourceWithClient(metadata metadataClient) *DatasetSource {
	return &DatasetSource{metadata: metadata}
}

// ListSubjects returns active dataset subjects enriched with external symbols.
func (s *DatasetSource) ListSubjects(ctx context.Context, spaceID string, datasetID string, dataSourceID string) ([]domain.DatasetSubject, error) {
	if strings.TrimSpace(datasetID) == "" {
		return nil, fmt.Errorf("dataset_id is required")
	}
	bindings, err := s.listDatasetBindings(ctx, spaceID, datasetID)
	if err != nil {
		return nil, err
	}
	symbols, err := s.listSubjectSymbols(ctx, spaceID, dataSourceID)
	if err != nil {
		return nil, err
	}
	return mergeDatasetSubjects(bindings, symbols), nil
}

func (s *DatasetSource) listDatasetBindings(ctx context.Context, spaceID string, datasetID string) ([]*storagepb.DatasetSubject, error) {
	var all []*storagepb.DatasetSubject
	for page := uint32(1); ; page++ {
		rsp, err := s.metadata.ListDatasetSubjects(ctx, &storagepb.ListDatasetSubjectsReq{
			SpaceId:   spaceID,
			DatasetId: datasetID,
			Page:      &storagepb.Page{Page: page, Size: storagePageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("list dataset subjects: %w", err)
		}
		if err := ensureStorageOK("list dataset subjects", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		all = append(all, rsp.GetDatasetSubjects()...)
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	return all, nil
}

func (s *DatasetSource) listSubjectSymbols(ctx context.Context, spaceID string, dataSourceID string) (map[string]string, error) {
	symbols := make(map[string]string)
	for page := uint32(1); ; page++ {
		rsp, err := s.metadata.ListSubjectSymbols(ctx, &storagepb.ListSubjectSymbolsReq{
			SpaceId:      spaceID,
			DataSourceId: dataSourceID,
			Page:         &storagepb.Page{Page: page, Size: storagePageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("list subject symbols: %w", err)
		}
		if err := ensureStorageOK("list subject symbols", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		for _, item := range rsp.GetSubjectSymbols() {
			if item.GetSubjectId() == "" || isInactive(item.GetStatus()) {
				continue
			}
			if item.GetExternalSymbol() != "" {
				symbols[item.GetSubjectId()] = item.GetExternalSymbol()
			}
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() {
			break
		}
	}
	return symbols, nil
}

func mergeDatasetSubjects(bindings []*storagepb.DatasetSubject, symbols map[string]string) []domain.DatasetSubject {
	subjects := make([]domain.DatasetSubject, 0, len(bindings))
	for _, binding := range bindings {
		if binding.GetSubjectId() == "" || isInactive(binding.GetStatus()) {
			continue
		}
		external := binding.GetAttributes()["external_symbol"]
		if external == "" {
			external = symbols[binding.GetSubjectId()]
		}
		if external == "" {
			external = binding.GetSubjectId()
		}
		subjects = append(subjects, domain.DatasetSubject{
			SubjectID:      binding.GetSubjectId(),
			SubjectName:    binding.GetSubjectId(),
			ExternalSymbol: external,
			Status:         binding.GetStatus(),
		})
	}
	return subjects
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

func isInactive(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "active", "enabled":
		return false
	default:
		return true
	}
}

func normalizeTRPCTarget(raw string, defaultPort string) string {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "ip://127.0.0.1:" + defaultPort
	}
	if strings.HasPrefix(raw, "ip://") {
		return raw
	}
	parsed, err := url.Parse(raw)
	if err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") {
		return raw
	}
	if err == nil && parsed.Scheme != "" && parsed.Scheme != "http" && parsed.Scheme != "https" {
		return raw
	}
	if err == nil && parsed.Host != "" {
		return "ip://" + parsed.Host
	}
	if strings.Contains(raw, "://") || !strings.Contains(raw, ":") {
		return raw
	}
	return "ip://" + raw
}
