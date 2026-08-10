package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
)

type bindingContractClient interface {
	ListViews(context.Context, *storagepb.ListViewsReq) (*storagepb.ListViewsRsp, error)
}

func validateCandidateBindingSet(bindings []domain.FactorBinding) error {
	if err := validateLegacyCandidateBindingSet(bindings); err != nil {
		return err
	}
	seen := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		if binding.Status == domain.BindingStatusDisabled || binding.Status == domain.BindingStatusCleanupPending {
			continue
		}
		key := strings.Join([]string{binding.FactorID, binding.SpaceID, binding.SourceViewID, binding.Freq}, "\x00")
		if existing, ok := seen[key]; ok && existing != binding.BindingID {
			return fmt.Errorf("duplicate binding scope for source view %s/%s frequency %s", binding.SpaceID, binding.SourceViewID, binding.Freq)
		}
		seen[key] = binding.BindingID
	}
	return nil
}

// ValidateEnabledBinding validates the active Source View read contract.
func (s *MetadataSync) ValidateEnabledBinding(ctx context.Context, binding domain.FactorBinding, factor domain.FactorDef) error {
	_, supportsView := s.client.(viewMetadataClient)
	if binding.SourceDataset != "" && !supportsView {
		return s.validateLegacyEnabledBinding(ctx, binding, factor)
	}
	if _, err := domain.ParseFrequency(binding.Freq); err != nil {
		return fmt.Errorf("invalid frequency %q: %w", binding.Freq, err)
	}
	if s == nil || s.client == nil {
		return fmt.Errorf("Storage metadata client is required to enable a binding")
	}
	view, err := s.getView(ctx, binding.SpaceID, binding.SourceViewID)
	if err != nil {
		return fmt.Errorf("load source view %s/%s: %w", binding.SpaceID, binding.SourceViewID, err)
	}
	if view.GetStatus() != "active" || strings.TrimSpace(view.GetActiveIndexId()) == "" {
		return fmt.Errorf("source view %s/%s must have an active index", binding.SpaceID, binding.SourceViewID)
	}
	if err := validateFactorLookbackKeepDuration(view.GetKeepDuration(), binding.Freq, factor.LookbackPeriods); err != nil {
		return fmt.Errorf("source view %s/%s: %w", binding.SpaceID, binding.SourceViewID, err)
	}
	if err := validateViewFrequency(view.GetFilterJson(), binding.Freq); err != nil {
		return fmt.Errorf("source view %s/%s: %w", binding.SpaceID, binding.SourceViewID, err)
	}
	if strings.TrimSpace(view.GetPrimaryDatasetId()) == "" {
		return fmt.Errorf("source view %s/%s primary_dataset_id is required", binding.SpaceID, binding.SourceViewID)
	}
	for _, datasetID := range view.GetDatasetIds() {
		dataset, loadErr := s.getDataset(ctx, binding.SpaceID, datasetID)
		if loadErr != nil {
			return fmt.Errorf("load source view dataset %s/%s: %w", binding.SpaceID, datasetID, loadErr)
		}
		if strings.TrimSpace(dataset.GetAttributes()["dataset_role"]) == "factor_result" {
			return fmt.Errorf("source view %s/%s contains forbidden factor_result dataset %s", binding.SpaceID, binding.SourceViewID, datasetID)
		}
		if dataset.GetStatus() != "active" {
			return fmt.Errorf("source view dataset %s/%s must be active", binding.SpaceID, datasetID)
		}
		if !containsFrequency(dataset.GetFreqs(), binding.Freq) {
			return fmt.Errorf("source view dataset %s/%s does not support frequency %s", binding.SpaceID, datasetID, binding.Freq)
		}
	}
	return validateActiveViewInputs(view, factor.InputColumns)
}

// validateFactorLookbackKeepDuration allows finite Source Views when they
// retain enough history for the factor. This matches the personal deployment
// default (for example, 720h for 1m bars) while still rejecting a window that
// cannot satisfy the declared lookback.
func validateFactorLookbackKeepDuration(keep, frequency string, lookbackPeriods int) error {
	keep = strings.TrimSpace(keep)
	if keep == "" || keep == "0" || lookbackPeriods <= 0 {
		return nil
	}
	keepDuration, err := time.ParseDuration(keep)
	if err != nil || keepDuration <= 0 {
		if err == nil {
			err = fmt.Errorf("duration must be positive")
		}
		return fmt.Errorf("invalid keep_duration %q: %w", keep, err)
	}
	periodDuration, err := domain.ParseFrequency(frequency)
	if err != nil {
		return fmt.Errorf("invalid frequency %q: %w", frequency, err)
	}
	required := periodDuration * time.Duration(lookbackPeriods)
	if required > 0 && keepDuration < required {
		return fmt.Errorf("keep_duration %s is shorter than lookback window %s", keep, required)
	}
	return nil
}

func containsFrequency(values []string, expected string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) == strings.TrimSpace(expected) {
			return true
		}
	}
	return false
}

func validateLegacyCandidateBindingSet(bindings []domain.FactorBinding) error {
	targets := make(map[string]string, len(bindings))
	for _, binding := range bindings {
		if binding.Status == domain.BindingStatusEnabled {
			targets[binding.SpaceID+"\x00"+binding.TargetDataset] = binding.BindingID
		}
	}
	for _, binding := range bindings {
		if binding.Status != domain.BindingStatusEnabled {
			continue
		}
		if target, ok := targets[binding.SpaceID+"\x00"+binding.SourceDataset]; ok {
			return fmt.Errorf("source dataset %s/%s is also targeted by enabled binding %q", binding.SpaceID, binding.SourceDataset, target)
		}
	}
	return nil
}

func (s *MetadataSync) validateLegacyEnabledBinding(ctx context.Context, binding domain.FactorBinding, factor domain.FactorDef) error {
	if binding.SourceDataset == binding.TargetDataset {
		return fmt.Errorf("source_dataset and target_dataset must differ")
	}
	if _, err := domain.ParseFrequency(binding.Freq); err != nil {
		return fmt.Errorf("invalid frequency %q: %w", binding.Freq, err)
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
	return validateLegacyActiveViewProjection(ctx, client, s.auth, binding, factor.InputColumns)
}

func validateLegacyActiveViewProjection(ctx context.Context, client bindingContractClient, auth *commonpb.AuthInfo, binding domain.FactorBinding, inputs []string) error {
	var missingByView []string
	for page := uint32(1); ; page++ {
		rsp, err := client.ListViews(ctx, &storagepb.ListViewsReq{AuthInfo: auth, SpaceId: binding.SpaceID, DatasetId: binding.SourceDataset, Status: "active", Page: &commonpb.Page{Page: page, Size: 500}})
		if err != nil {
			return err
		}
		if !retOK(rsp.GetRetInfo()) {
			return retInfoError("ListViews", rsp.GetRetInfo())
		}
		for _, view := range rsp.GetViews() {
			if view.GetStatus() != "active" || strings.TrimSpace(view.GetActiveIndexId()) == "" || view.GetPrimaryDatasetId() != binding.SourceDataset {
				continue
			}
			available := map[string]struct{}{}
			for _, column := range view.GetActiveColumns() {
				available[column.GetColumnName()] = struct{}{}
				available[column.GetOriginId()] = struct{}{}
			}
			var missing []string
			for _, input := range inputs {
				if _, ok := available[binding.SourceDataset+"."+input]; !ok {
					missing = append(missing, input)
				}
			}
			if len(missing) == 0 {
				return nil
			}
			missingByView = append(missingByView, fmt.Sprintf("%s missing %s", view.GetViewId(), strings.Join(missing, ",")))
		}
		if rsp.GetPageResult() == nil || !rsp.GetPageResult().GetHasMore() || len(rsp.GetViews()) == 0 {
			break
		}
	}
	if len(missingByView) == 0 {
		return fmt.Errorf("source dataset %s/%s has no active primary view", binding.SpaceID, binding.SourceDataset)
	}
	return fmt.Errorf("no active view projects all input_columns: %s", strings.Join(missingByView, "; "))
}

func validateActiveViewInputs(view *storagepb.View, inputs []string) error {
	exact := make(map[string]map[string]struct{})
	suffix := make(map[string]map[string]struct{})
	for _, column := range view.GetActiveColumns() {
		identity := strings.TrimSpace(column.GetColumnName())
		if identity == "" {
			identity = strings.TrimSpace(column.GetOriginId())
		}
		if identity == "" {
			continue
		}
		name := strings.TrimSpace(column.GetColumnName())
		if name == "" {
			continue
		}
		if exact[name] == nil {
			exact[name] = make(map[string]struct{})
		}
		exact[name][identity] = struct{}{}
		if _, nameSuffix, ok := strings.Cut(name, "."); ok {
			if suffix[nameSuffix] == nil {
				suffix[nameSuffix] = make(map[string]struct{})
			}
			suffix[nameSuffix][identity] = struct{}{}
		}
	}
	for _, input := range inputs {
		if len(exact[input]) > 0 {
			if !strings.Contains(input, ".") && len(exact[input]) > 1 {
				return fmt.Errorf("source view %s input %s is ambiguous; use a qualified column", view.GetViewId(), input)
			}
			continue
		}
		if strings.Contains(input, ".") {
			return fmt.Errorf("source view %s active schema is missing input %s", view.GetViewId(), input)
		}
		// Unqualified inputs are mapped back from dataset-qualified View
		// columns during read. Reject a suffix shared by multiple datasets so
		// the binding cannot be enabled only to fail on every execution.
		if len(suffix[input]) > 1 {
			return fmt.Errorf("source view %s input %s is ambiguous; use a qualified column", view.GetViewId(), input)
		}
		if len(suffix[input]) == 0 {
			return fmt.Errorf("source view %s active schema is missing input %s", view.GetViewId(), input)
		}
	}
	return nil
}

func validateViewFrequency(raw, expected string) error {
	var filter map[string]any
	if err := json.Unmarshal([]byte(raw), &filter); err != nil {
		return fmt.Errorf("invalid filter_json: %w", err)
	}
	actual, _ := filter["freq"].(string)
	if strings.TrimSpace(actual) != strings.TrimSpace(expected) {
		return fmt.Errorf("filter_json frequency %q does not match binding frequency %q", actual, expected)
	}
	return nil
}
