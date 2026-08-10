package command

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/spf13/cobra"
)

const defaultSetupConfigDir = "./examples/setup/default"

type setupInitBundle struct {
	Spaces   []setupclient.Space
	Datasets []seedDataset
	Calls    []metadataImportCall
}

type setupInitStorage interface {
	Apply(context.Context, []metadataImportCall) (metadataImportSummary, error)
	Activate(context.Context, []seedDataset) (setupDatasetActivationSummary, error)
	Verify(context.Context, []metadataImportCall) (metadataImportSummary, error)
	Close() error
}

type setupDatasetActivationSummary struct {
	Total     int `json:"total"`
	Activated int `json:"activated"`
	Unchanged int `json:"unchanged"`
}

type setupInitMetadataCounts struct {
	Planned   int `json:"planned"`
	Applied   int `json:"applied,omitempty"`
	Unchanged int `json:"unchanged"`
}

type setupInitSummary struct {
	Status           string                        `json:"status"`
	BusinessSpaces   int                           `json:"business_spaces"`
	BusinessSpaceIDs []string                      `json:"business_space_ids"`
	Admin            setupclient.ApplyResult       `json:"admin"`
	AdminState       string                        `json:"admin_state"`
	LoginAPI         string                        `json:"login_api"`
	Metadata         setupInitMetadataCounts       `json:"metadata"`
	Datasets         setupDatasetActivationSummary `json:"datasets"`
	Verification     setupInitMetadataCounts       `json:"verification"`
	Factors          *setupFactorSummary           `json:"factors,omitempty"`
}

func newSetupInitCommand(deps setupDeps) *cobra.Command {
	var file, configDir, storageHost string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "导入默认空间、数据集、字段和因子",
		RunE: func(cmd *cobra.Command, _ []string) error {
			bundle, err := deps.loadInitBundle(configDir)
			if err != nil {
				return err
			}
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)

			admin, err := deps.applySpaces(cmd.Context(), snapshot, bundle.Spaces)
			if err != nil {
				return err
			}
			adminStatus, err := deps.statusSpaces(cmd.Context(), snapshot, bundle.Spaces)
			if err != nil {
				return err
			}
			if adminStatus.State != "completed" || adminStatus.Spaces != len(bundle.Spaces) {
				return fmt.Errorf("setup_incomplete")
			}
			login, err := deps.login(cmd.Context(), snapshot)
			if err != nil {
				return err
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}

			storage, err := deps.openInitStorage(cmd.Context(), snapshot, storageHost)
			if err != nil {
				return err
			}
			defer storage.Close()
			metadata, err := storage.Apply(cmd.Context(), bundle.Calls)
			if err != nil {
				return err
			}
			datasets, err := storage.Activate(cmd.Context(), bundle.Datasets)
			if err != nil {
				return err
			}
			verification, err := storage.Verify(cmd.Context(), bundle.Calls)
			if err != nil {
				return err
			}
			if verification.Applied != 0 || verification.Unchanged != len(bundle.Calls) {
				return fmt.Errorf("metadata_verification_failed")
			}
			factorItems, err := loadSetupFactors(snapshot.Manifest, filepath.Dir(file))
			if err != nil {
				return err
			}
			var factorSummary *setupFactorSummary
			if len(factorItems) > 0 {
				factorService, openErr := deps.openInitFactor(cmd.Context(), snapshot)
				if openErr != nil {
					return openErr
				}
				appliedSummary, applyErr := factorService.Apply(cmd.Context(), sortedFactorItems(factorItems))
				closeErr := factorService.Close()
				if applyErr != nil {
					return applyErr
				}
				if closeErr != nil {
					return closeErr
				}
				factorSummary = &appliedSummary
			}
			adminStatus, err = deps.statusSpaces(cmd.Context(), snapshot, bundle.Spaces)
			if err != nil {
				return err
			}
			if adminStatus.State != "completed" || adminStatus.Spaces != len(bundle.Spaces) {
				return fmt.Errorf("setup_incomplete")
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			return writeSetupJSON(cmd, setupInitSummary{
				Status: "ready", BusinessSpaces: len(bundle.Spaces), BusinessSpaceIDs: setupSpaceIDs(bundle.Spaces),
				Admin: admin, AdminState: adminStatus.State, LoginAPI: login.LoginAPI,
				Metadata: setupInitMetadataCounts{
					Planned: metadata.Planned, Applied: metadata.Applied, Unchanged: metadata.Unchanged,
				},
				Datasets: datasets,
				Verification: setupInitMetadataCounts{
					Planned: verification.Planned, Unchanged: verification.Unchanged,
				},
				Factors: factorSummary,
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&configDir, "config-dir", defaultSetupConfigDir, "默认配置目录")
	cmd.Flags().StringVar(&storageHost, "storage-host", "", "已部署 Storage 的主机名称")
	_ = cmd.MarkFlagRequired("storage-host")
	return cmd
}

// newSetupFactorsCommand imports the repository's Python factor definitions
// without touching Storage metadata.  It is useful when metadata has already
// been initialized (possibly from an older seed) and a full setup init would
// correctly refuse to overwrite that existing contract.
func newSetupFactorsCommand(deps setupDeps) *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "factors",
		Short: "导入本地 Python 因子并建立默认 View 绑定",
		RunE: func(cmd *cobra.Command, _ []string) error {
			snapshot, err := deps.load(file)
			if err != nil {
				return err
			}
			defer clearSetupSecrets(snapshot)
			items, err := loadSetupFactors(snapshot.Manifest, filepath.Dir(file))
			if err != nil {
				return err
			}
			summary := setupFactorSummary{Enabled: len(items) > 0, Planned: len(items)}
			if len(items) > 0 {
				service, openErr := deps.openInitFactor(cmd.Context(), snapshot)
				if openErr != nil {
					return openErr
				}
				summary, err = service.Apply(cmd.Context(), sortedFactorItems(items))
				closeErr := service.Close()
				if err != nil {
					return err
				}
				if closeErr != nil {
					return closeErr
				}
			}
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			return writeSetupJSON(cmd, map[string]any{"status": "ready", "factors": summary})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	return cmd
}

func setupSpaceIDs(spaces []setupclient.Space) []string {
	ids := make([]string, 0, len(spaces))
	for _, space := range spaces {
		ids = append(ids, strings.TrimSpace(space.SpaceID))
	}
	return ids
}

func loadSetupInitBundle(configDir string) (setupInitBundle, error) {
	configDir = strings.TrimSpace(configDir)
	if configDir == "" {
		return setupInitBundle{}, fmt.Errorf("setup_config_dir_invalid")
	}
	seed, err := loadMetadataSeed(filepath.Join(configDir, "metadata.yaml"))
	if err != nil {
		return setupInitBundle{}, err
	}
	if err := validateReservedInternalSpaces(seed); err != nil {
		return setupInitBundle{}, err
	}
	spaces, err := businessSetupSpaces(seed)
	if err != nil {
		return setupInitBundle{}, err
	}
	if err := validateSetupMetadataDependencies(seed); err != nil {
		return setupInitBundle{}, err
	}
	calls, err := buildMetadataImportCalls(seed)
	if err != nil {
		return setupInitBundle{}, err
	}
	datasets := append([]seedDataset(nil), seed.Datasets...)
	sort.Slice(datasets, func(i, j int) bool {
		if datasets[i].SpaceID == datasets[j].SpaceID {
			return datasets[i].DatasetID < datasets[j].DatasetID
		}
		return datasets[i].SpaceID < datasets[j].SpaceID
	})
	return setupInitBundle{Spaces: spaces, Datasets: datasets, Calls: calls}, nil
}

func validateSetupMetadataDependencies(seed metadataSeed) error {
	if len(seed.Subjects) > 0 || len(seed.SubjectSymbols) > 0 || len(seed.DatasetSubjects) > 0 {
		return fmt.Errorf("default setup metadata cannot contain runtime subject catalog entries")
	}
	spaces := make(map[string]struct{}, len(seed.Spaces))
	for _, item := range seed.Spaces {
		if err := addSetupMetadataKey(spaces, item.SpaceID, "space"); err != nil {
			return err
		}
	}
	requireSpace := func(resource, spaceID string) error {
		if _, ok := spaces[strings.TrimSpace(spaceID)]; !ok {
			return fmt.Errorf("%s references undefined space %q", resource, spaceID)
		}
		return nil
	}

	sources := make(map[string]struct{}, len(seed.DataSources))
	for _, item := range seed.DataSources {
		if err := requireSpace("data_source", item.SpaceID); err != nil {
			return err
		}
		if err := addSetupMetadataKey(sources, setupMetadataKey(item.SpaceID, item.DataSourceID), "data_source"); err != nil {
			return err
		}
	}
	datasets := make(map[string]struct{}, len(seed.Datasets))
	datasetDefinitions := make(map[string]seedDataset, len(seed.Datasets))
	for _, item := range seed.Datasets {
		if err := requireSpace("dataset", item.SpaceID); err != nil {
			return err
		}
		if _, ok := sources[setupMetadataKey(item.SpaceID, item.DataSourceID)]; !ok {
			return fmt.Errorf("dataset %s/%s references undefined data_source %q", item.SpaceID, item.DatasetID, item.DataSourceID)
		}
		if err := addSetupMetadataKey(datasets, setupMetadataKey(item.SpaceID, item.DatasetID), "dataset"); err != nil {
			return err
		}
		datasetDefinitions[setupMetadataKey(item.SpaceID, item.DatasetID)] = item
	}
	groups := make(map[string]struct{}, len(seed.FieldGroups))
	for _, item := range seed.FieldGroups {
		if err := requireSpace("field_group", item.SpaceID); err != nil {
			return err
		}
		if err := addSetupMetadataKey(groups, setupMetadataKey(item.SpaceID, item.GroupID), "field_group"); err != nil {
			return err
		}
	}
	fields := make(map[string]struct{}, len(seed.Fields))
	for _, item := range seed.Fields {
		if err := requireSpace("field", item.SpaceID); err != nil {
			return err
		}
		if strings.TrimSpace(item.GroupID) != "" {
			if _, ok := groups[setupMetadataKey(item.SpaceID, item.GroupID)]; !ok {
				return fmt.Errorf("field %s/%s references undefined field_group %q", item.SpaceID, item.FieldID, item.GroupID)
			}
		}
		if err := addSetupMetadataKey(fields, setupMetadataKey(item.SpaceID, item.FieldID), "field"); err != nil {
			return err
		}
	}
	factors := make(map[string]struct{}, len(seed.Factors))
	for _, item := range seed.Factors {
		if err := requireSpace("factor", item.SpaceID); err != nil {
			return err
		}
		if err := addSetupMetadataKey(factors, setupMetadataKey(item.SpaceID, item.FactorID), "factor"); err != nil {
			return err
		}
	}
	datasetColumns := make(map[string]struct{}, len(seed.DatasetColumns))
	for _, item := range seed.DatasetColumns {
		if _, ok := datasets[setupMetadataKey(item.SpaceID, item.DatasetID)]; !ok {
			return fmt.Errorf("dataset_column references undefined dataset %s/%s", item.SpaceID, item.DatasetID)
		}
		columnKey := setupMetadataColumnKey(item.SpaceID, item.DatasetID, item.ColumnName)
		if _, ok := datasetColumns[columnKey]; ok {
			return fmt.Errorf(
				"duplicate metadata dataset_column %s/%s/%s",
				item.SpaceID,
				item.DatasetID,
				item.ColumnName,
			)
		}
		datasetColumns[columnKey] = struct{}{}
		originType, err := parseDatasetColumnOriginType(item.OriginType)
		if err != nil {
			return err
		}
		switch originType {
		case storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FIELD:
			if _, ok := fields[setupMetadataKey(item.SpaceID, item.OriginID)]; !ok {
				return fmt.Errorf(
					"dataset_column %s/%s/%s references undefined field %q",
					item.SpaceID,
					item.DatasetID,
					item.ColumnName,
					item.OriginID,
				)
			}
		case storagepb.DatasetColumnOriginType_DATASET_COLUMN_ORIGIN_TYPE_FACTOR:
			if _, ok := factors[setupMetadataKey(item.SpaceID, item.OriginID)]; !ok {
				return fmt.Errorf(
					"dataset_column %s/%s/%s references undefined factor %q",
					item.SpaceID,
					item.DatasetID,
					item.ColumnName,
					item.OriginID,
				)
			}
		}
	}
	views := make(map[string]struct{}, len(seed.Views))
	viewDatasets := make(map[string]map[string]struct{}, len(seed.Views))
	for _, item := range seed.Views {
		if err := requireSpace("view", item.SpaceID); err != nil {
			return err
		}
		primaryDatasetID := strings.TrimSpace(item.PrimaryDatasetID)
		if primaryDatasetID == "" || item.PrimaryDatasetID != primaryDatasetID {
			return fmt.Errorf(
				"view %s/%s primary_dataset_id must be explicit and trimmed",
				item.SpaceID,
				item.ViewID,
			)
		}
		viewKey := setupMetadataKey(item.SpaceID, item.ViewID)
		allowedDatasets := make(map[string]struct{}, len(item.DatasetIDs)+1)
		for _, datasetID := range append([]string{item.PrimaryDatasetID}, item.DatasetIDs...) {
			datasetID = strings.TrimSpace(datasetID)
			if datasetID == "" {
				continue
			}
			if _, ok := datasets[setupMetadataKey(item.SpaceID, datasetID)]; !ok {
				return fmt.Errorf("view %s/%s references undefined dataset %q", item.SpaceID, item.ViewID, datasetID)
			}
			allowedDatasets[datasetID] = struct{}{}
		}
		if err := validateCanonicalSetupView(item, datasetDefinitions); err != nil {
			return err
		}
		if err := addSetupMetadataKey(views, viewKey, "view"); err != nil {
			return err
		}
		viewDatasets[viewKey] = allowedDatasets
	}
	viewColumns := make(map[string]struct{}, len(seed.ViewColumns))
	for _, item := range seed.ViewColumns {
		if _, ok := views[setupMetadataKey(item.SpaceID, item.ViewID)]; !ok {
			return fmt.Errorf("view_column references undefined view %s/%s", item.SpaceID, item.ViewID)
		}
		columnKey := setupMetadataColumnKey(item.SpaceID, item.ViewID, item.ColumnName)
		if _, ok := viewColumns[columnKey]; ok {
			return fmt.Errorf(
				"duplicate metadata view_column %s/%s/%s",
				item.SpaceID,
				item.ViewID,
				item.ColumnName,
			)
		}
		viewColumns[columnKey] = struct{}{}
		originType, err := parseColumnOriginType(item.OriginType)
		if err != nil {
			return err
		}
		if originType != storagepb.ColumnOriginType_COLUMN_ORIGIN_TYPE_DATASET_COLUMN {
			continue
		}
		originParts := strings.Split(strings.TrimSpace(item.OriginID), ".")
		if len(originParts) != 2 {
			return fmt.Errorf(
				"view_column %s/%s/%s has invalid dataset_column origin %q",
				item.SpaceID,
				item.ViewID,
				item.ColumnName,
				item.OriginID,
			)
		}
		if _, ok := viewDatasets[setupMetadataKey(item.SpaceID, item.ViewID)][originParts[0]]; !ok {
			return fmt.Errorf(
				"view_column %s/%s/%s references dataset %q not declared by view",
				item.SpaceID,
				item.ViewID,
				item.ColumnName,
				originParts[0],
			)
		}
		if _, ok := datasetColumns[setupMetadataColumnKey(item.SpaceID, originParts[0], originParts[1])]; !ok {
			return fmt.Errorf(
				"view_column %s/%s/%s references undefined dataset_column %q",
				item.SpaceID,
				item.ViewID,
				item.ColumnName,
				item.OriginID,
			)
		}
	}
	return nil
}

func addSetupMetadataKey(seen map[string]struct{}, raw, resource string) error {
	key := strings.TrimSpace(raw)
	if key == "" {
		return fmt.Errorf("%s id is required", resource)
	}
	if _, ok := seen[key]; ok {
		return fmt.Errorf("duplicate metadata %s %q", resource, key)
	}
	seen[key] = struct{}{}
	return nil
}

func setupMetadataKey(spaceID, id string) string {
	return strings.TrimSpace(spaceID) + "\x00" + strings.TrimSpace(id)
}

func setupMetadataColumnKey(spaceID, parentID, columnName string) string {
	return strings.Join([]string{
		strings.TrimSpace(spaceID),
		strings.TrimSpace(parentID),
		strings.TrimSpace(columnName),
	}, "\x00")
}

func validateCanonicalSetupView(view seedView, datasets map[string]seedDataset) error {
	normalized, err := canonicalMetadataView(view, datasets)
	if err != nil {
		return err
	}
	if !slices.Equal(view.DatasetIDs, normalized.DatasetIDs) {
		return fmt.Errorf(
			"view %s/%s dataset_ids must be canonical with primary_dataset_id first",
			view.SpaceID,
			view.ViewID,
		)
	}
	if !slices.Equal(view.GrainKeys, normalized.GrainKeys) {
		return fmt.Errorf("view %s/%s grain_keys must be canonical", view.SpaceID, view.ViewID)
	}
	if view.Engine != normalized.Engine {
		return fmt.Errorf(
			"view %s/%s engine must be %q",
			view.SpaceID,
			view.ViewID,
			normalized.Engine,
		)
	}
	if view.FilterJSON != normalized.FilterJSON {
		return fmt.Errorf("view %s/%s filter_json must be canonical", view.SpaceID, view.ViewID)
	}
	return nil
}

func canonicalMetadataView(view seedView, datasets map[string]seedDataset) (seedView, error) {
	primaryDatasetID := strings.TrimSpace(view.PrimaryDatasetID)
	if primaryDatasetID == "" && len(view.DatasetIDs) > 0 {
		primaryDatasetID = strings.TrimSpace(view.DatasetIDs[0])
	}
	if primaryDatasetID == "" {
		return seedView{}, fmt.Errorf("view %s/%s primary_dataset_id is required", view.SpaceID, view.ViewID)
	}
	normalizedDatasetIDs := canonicalSetupViewDatasetIDs(primaryDatasetID, view.DatasetIDs)
	primary, ok := datasets[setupMetadataKey(view.SpaceID, primaryDatasetID)]
	if !ok {
		return seedView{}, fmt.Errorf(
			"view %s/%s primary dataset %q must be included in the metadata seed",
			view.SpaceID,
			view.ViewID,
			primaryDatasetID,
		)
	}
	dataKind, err := parseDataKind(primary.DataKind)
	if err != nil {
		return seedView{}, fmt.Errorf("view %s/%s primary dataset: %w", view.SpaceID, view.ViewID, err)
	}
	grainKeys := []string{"record_id", "version"}
	engine := "bleve"
	if dataKind == storagepb.DataKind_DATA_KIND_TIME_SERIES {
		grainKeys = []string{"subject_id", "freq", "data_time", "series_tag"}
		engine = "duckdb"
	}
	filterJSON := view.FilterJSON
	if dataKind == storagepb.DataKind_DATA_KIND_TIME_SERIES {
		freq, normalizedFilter, err := canonicalSetupTimeSeriesFilter(view.FilterJSON)
		if err != nil {
			return seedView{}, fmt.Errorf("view %s/%s: %w", view.SpaceID, view.ViewID, err)
		}
		for _, datasetID := range normalizedDatasetIDs {
			dataset, exists := datasets[setupMetadataKey(view.SpaceID, datasetID)]
			if !exists {
				return seedView{}, fmt.Errorf(
					"view %s/%s dataset %q must be included in the metadata seed",
					view.SpaceID,
					view.ViewID,
					datasetID,
				)
			}
			if kind, parseErr := parseDataKind(dataset.DataKind); parseErr == nil &&
				kind == storagepb.DataKind_DATA_KIND_TIME_SERIES &&
				!setupDatasetSupportsFreq(dataset.Freqs, freq) {
				return seedView{}, fmt.Errorf(
					"view %s/%s dataset %s does not support freq %q",
					view.SpaceID,
					view.ViewID,
					datasetID,
					freq,
				)
			}
		}
		filterJSON = normalizedFilter
	}
	view.PrimaryDatasetID = primaryDatasetID
	view.DatasetIDs = normalizedDatasetIDs
	view.GrainKeys = grainKeys
	view.Engine = engine
	view.FilterJSON = filterJSON
	return view, nil
}

func setupDatasetSupportsFreq(freqs []string, freq string) bool {
	for _, item := range freqs {
		if strings.TrimSpace(item) == freq {
			return true
		}
	}
	return false
}

func canonicalSetupViewDatasetIDs(primaryDatasetID string, datasetIDs []string) []string {
	seen := make(map[string]struct{}, len(datasetIDs)+1)
	result := make([]string, 0, len(datasetIDs)+1)
	add := func(datasetID string) {
		datasetID = strings.TrimSpace(datasetID)
		if datasetID == "" {
			return
		}
		if _, ok := seen[datasetID]; ok {
			return
		}
		seen[datasetID] = struct{}{}
		result = append(result, datasetID)
	}
	add(primaryDatasetID)
	for _, datasetID := range datasetIDs {
		add(datasetID)
	}
	return result
}

func canonicalSetupTimeSeriesFilter(raw string) (string, string, error) {
	fields := make(map[string]json.RawMessage)
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &fields); err != nil {
		return "", "", fmt.Errorf("invalid time series filter_json: %w", err)
	}
	var freq string
	if encoded, ok := fields["freq"]; ok {
		if err := json.Unmarshal(encoded, &freq); err != nil {
			return "", "", fmt.Errorf("filter_json.freq must be a string")
		}
	}
	freq = strings.TrimSpace(freq)
	if freq == "" {
		return "", "", fmt.Errorf("filter_json.freq is required")
	}
	encodedFreq, err := json.Marshal(freq)
	if err != nil {
		return "", "", err
	}
	fields["freq"] = encodedFreq
	normalized, err := json.Marshal(fields)
	if err != nil {
		return "", "", err
	}
	return freq, string(normalized), nil
}

type remoteSetupInitStorage struct {
	transport setupssh.Client
	session   *remoteStorageSession
}

func defaultOpenSetupInitStorage(
	ctx context.Context,
	snapshot *setupconfig.Snapshot,
	host string,
) (setupInitStorage, error) {
	_, transport, session, _, err := openRemoteStorage(ctx, snapshot, host)
	if err != nil {
		return nil, err
	}
	return &remoteSetupInitStorage{transport: transport, session: session}, nil
}

func (s *remoteSetupInitStorage) Apply(ctx context.Context, calls []metadataImportCall) (metadataImportSummary, error) {
	if s == nil || s.session == nil || s.session.listener == nil {
		return metadataImportSummary{}, fmt.Errorf("storage_not_reachable")
	}
	return runMetadataApply(ctx, "http://"+s.session.listener.Addr().String(), calls)
}

func (s *remoteSetupInitStorage) Activate(ctx context.Context, datasets []seedDataset) (setupDatasetActivationSummary, error) {
	if s == nil || s.session == nil {
		return setupDatasetActivationSummary{}, fmt.Errorf("storage_not_reachable")
	}
	return activateSetupDatasets(ctx, s.session.metadata, s.session.auth, datasets)
}

func (s *remoteSetupInitStorage) Verify(ctx context.Context, calls []metadataImportCall) (metadataImportSummary, error) {
	return s.Apply(ctx, calls)
}

func (s *remoteSetupInitStorage) Close() error {
	if s == nil {
		return nil
	}
	if s.session != nil {
		s.session.Close()
	}
	if s.transport != nil {
		return s.transport.Close()
	}
	return nil
}

type datasetActivationAPI interface {
	GetDataset(context.Context, *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error)
	CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error)
	ActivateDataset(context.Context, *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error)
}

func activateSetupDatasets(
	ctx context.Context,
	api datasetActivationAPI,
	auth *storagepb.AuthInfo,
	datasets []seedDataset,
) (setupDatasetActivationSummary, error) {
	result := setupDatasetActivationSummary{Total: len(datasets)}
	if api == nil {
		return result, fmt.Errorf("storage_metadata_unavailable")
	}
	for _, item := range datasets {
		spaceID, datasetID := strings.TrimSpace(item.SpaceID), strings.TrimSpace(item.DatasetID)
		current, err := api.GetDataset(ctx, &storagepb.GetDatasetReq{
			AuthInfo: auth, SpaceId: spaceID, DatasetId: datasetID,
		})
		if err != nil || current == nil || current.GetRetInfo() == nil ||
			current.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || current.GetDataset() == nil {
			return result, fmt.Errorf("dataset_activation_read_failed: %s/%s", spaceID, datasetID)
		}
		if current.GetDataset().GetStatus() == "active" && current.GetDataset().GetBindingLocked() {
			result.Unchanged++
			continue
		}
		check, err := api.CheckDatasetActivation(ctx, &storagepb.CheckDatasetActivationReq{
			AuthInfo: auth, SpaceId: spaceID, DatasetId: datasetID,
		})
		if err != nil || check == nil || check.GetRetInfo() == nil ||
			check.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return result, fmt.Errorf("dataset_activation_check_failed: %s/%s", spaceID, datasetID)
		}
		if !check.GetReady() {
			return result, fmt.Errorf(
				"dataset_activation_not_ready: %s/%s: %s",
				spaceID, datasetID, summarizeDatasetActivationChecks(check.GetChecks()),
			)
		}
		activated, err := api.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{
			AuthInfo: auth, SpaceId: spaceID, DatasetId: datasetID,
			ExpectedRevision: check.GetDatasetRevision(),
		})
		if err != nil || activated == nil || activated.GetRetInfo() == nil ||
			activated.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS ||
			activated.GetDataset() == nil ||
			activated.GetDataset().GetStatus() != "active" ||
			!activated.GetDataset().GetBindingLocked() {
			return result, fmt.Errorf("dataset_activation_failed: %s/%s", spaceID, datasetID)
		}
		result.Activated++
	}
	return result, nil
}

func summarizeDatasetActivationChecks(checks []*storagepb.DatasetActivationCheck) string {
	if len(checks) == 0 {
		return "no checks returned"
	}
	parts := make([]string, 0, len(checks))
	for _, check := range checks {
		if check == nil {
			continue
		}
		state := "not_ready"
		if check.GetReady() {
			state = "ready"
		}
		part := strings.TrimSpace(check.GetCheckId()) + "=" + state
		if summary := strings.TrimSpace(check.GetSummary()); summary != "" {
			part += "(" + summary + ")"
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		return "no checks returned"
	}
	return strings.Join(parts, ", ")
}

var _ setupInitStorage = (*remoteSetupInitStorage)(nil)
var _ datasetActivationAPI = (storageMetadataAPI)(nil)
