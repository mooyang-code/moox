package registry

import (
	"context"
	"fmt"
	"strings"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

type bindingContractClient interface {
	ListViews(context.Context, *storagepb.ListViewsReq) (*storagepb.ListViewsRsp, error)
}

// ValidateEnabledBinding validates the complete remote read contract before an
// enabled binding can become executable.
func (s *MetadataSync) ValidateEnabledBinding(
	ctx context.Context,
	binding domain.FactorBinding,
	factor domain.FactorDef,
) error {
	if binding.SourceDataset == binding.TargetDataset {
		return fmt.Errorf("source_dataset and target_dataset must differ")
	}
	if _, err := domain.ParseFrequency(binding.Freq); err != nil {
		return fmt.Errorf("invalid frequency %q: %w", binding.Freq, err)
	}
	if s == nil || s.client == nil {
		return fmt.Errorf("Storage metadata client is required to enable a binding")
	}
	client, ok := s.client.(bindingContractClient)
	if !ok {
		return fmt.Errorf("Storage metadata client does not support binding contract validation")
	}
	source, err := s.getDataset(ctx, binding.SpaceID, binding.SourceDataset)
	if err != nil {
		return fmt.Errorf("load source dataset %s/%s: %w", binding.SpaceID, binding.SourceDataset, err)
	}
	if source.GetStatus() != "active" {
		return fmt.Errorf("source dataset %s/%s must be active", binding.SpaceID, binding.SourceDataset)
	}
	if source.GetDataKind() != storagepb.DataKind_DATA_KIND_TIME_SERIES {
		return fmt.Errorf("source dataset %s/%s must be time-series", binding.SpaceID, binding.SourceDataset)
	}
	if strings.TrimSpace(source.GetAttributes()["dataset_role"]) == "factor_result" {
		return fmt.Errorf("source dataset %s/%s has forbidden dataset_role=factor_result", binding.SpaceID, binding.SourceDataset)
	}
	return validateActiveViewProjection(ctx, client, s.auth, binding, factor.InputColumns)
}

func validateActiveViewProjection(
	ctx context.Context,
	client bindingContractClient,
	auth *commonpb.AuthInfo,
	binding domain.FactorBinding,
	inputColumns []string,
) error {
	const pageSize uint32 = 500
	missingByView := make([]string, 0)
	for page := uint32(1); ; page++ {
		rsp, err := client.ListViews(ctx, &storagepb.ListViewsReq{
			AuthInfo: auth, SpaceId: binding.SpaceID, DatasetId: binding.SourceDataset,
			Status: "active", Page: &commonpb.Page{Page: page, Size: pageSize},
		})
		if err != nil {
			return fmt.Errorf("list active views for %s/%s: %w", binding.SpaceID, binding.SourceDataset, err)
		}
		if !retOK(rsp.GetRetInfo()) {
			return retInfoError("ListViews", rsp.GetRetInfo())
		}
		for _, view := range rsp.GetViews() {
			if view.GetStatus() != "active" || strings.TrimSpace(view.GetActiveIndexId()) == "" {
				continue
			}
			available := make(map[string]struct{}, len(view.GetActiveColumns()))
			for _, column := range view.GetActiveColumns() {
				available[column.GetColumnName()] = struct{}{}
				available[column.GetOriginId()] = struct{}{}
			}
			missing := make([]string, 0)
			for _, input := range inputColumns {
				qualified := binding.SourceDataset + "." + input
				if _, ok := available[qualified]; !ok {
					missing = append(missing, input)
				}
			}
			if len(missing) == 0 {
				return nil
			}
			missingByView = append(missingByView, fmt.Sprintf("%s missing %s", view.GetViewId(), strings.Join(missing, ",")))
		}
		pageResult := rsp.GetPageResult()
		if pageResult == nil || !pageResult.GetHasMore() || len(rsp.GetViews()) == 0 {
			break
		}
	}
	if len(missingByView) == 0 {
		return fmt.Errorf("source dataset %s/%s has no active view", binding.SpaceID, binding.SourceDataset)
	}
	return fmt.Errorf("no active view projects all input_columns: %s", strings.Join(missingByView, "; "))
}
