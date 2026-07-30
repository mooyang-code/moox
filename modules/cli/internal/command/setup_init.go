package command

import (
	"context"
	"fmt"
	"path/filepath"
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
	Status         string                        `json:"status"`
	BusinessSpaces int                           `json:"business_spaces"`
	Admin          setupclient.ApplyResult       `json:"admin"`
	AdminState     string                        `json:"admin_state"`
	LoginAPI       string                        `json:"login_api"`
	Metadata       setupInitMetadataCounts       `json:"metadata"`
	Datasets       setupDatasetActivationSummary `json:"datasets"`
	Verification   setupInitMetadataCounts       `json:"verification"`
}

func newSetupInitCommand(deps setupDeps) *cobra.Command {
	var file, configDir, storageHost string
	cmd := &cobra.Command{
		Use:   "init",
		Short: "导入默认空间、数据集和字段",
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
			if err := snapshot.VerifyUnchanged(); err != nil {
				return fmt.Errorf("config_changed")
			}
			return writeSetupJSON(cmd, setupInitSummary{
				Status: "ready", BusinessSpaces: len(bundle.Spaces),
				Admin: admin, AdminState: adminStatus.State, LoginAPI: login.LoginAPI,
				Metadata: setupInitMetadataCounts{
					Planned: metadata.Planned, Applied: metadata.Applied, Unchanged: metadata.Unchanged,
				},
				Datasets: datasets,
				Verification: setupInitMetadataCounts{
					Planned: verification.Planned, Unchanged: verification.Unchanged,
				},
			})
		},
	}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&configDir, "config-dir", defaultSetupConfigDir, "默认配置目录")
	cmd.Flags().StringVar(&storageHost, "storage-host", "", "已部署 Storage 的主机名称")
	_ = cmd.MarkFlagRequired("storage-host")
	return cmd
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
	for _, item := range seed.Factors {
		if err := requireSpace("factor", item.SpaceID); err != nil {
			return err
		}
	}
	for _, item := range seed.DatasetColumns {
		if _, ok := datasets[setupMetadataKey(item.SpaceID, item.DatasetID)]; !ok {
			return fmt.Errorf("dataset_column references undefined dataset %s/%s", item.SpaceID, item.DatasetID)
		}
	}
	views := make(map[string]struct{}, len(seed.Views))
	for _, item := range seed.Views {
		if err := requireSpace("view", item.SpaceID); err != nil {
			return err
		}
		for _, datasetID := range append([]string{item.PrimaryDatasetID}, item.DatasetIDs...) {
			if strings.TrimSpace(datasetID) == "" {
				continue
			}
			if _, ok := datasets[setupMetadataKey(item.SpaceID, datasetID)]; !ok {
				return fmt.Errorf("view %s/%s references undefined dataset %q", item.SpaceID, item.ViewID, datasetID)
			}
		}
		if err := addSetupMetadataKey(views, setupMetadataKey(item.SpaceID, item.ViewID), "view"); err != nil {
			return err
		}
	}
	for _, item := range seed.ViewColumns {
		if _, ok := views[setupMetadataKey(item.SpaceID, item.ViewID)]; !ok {
			return fmt.Errorf("view_column references undefined view %s/%s", item.SpaceID, item.ViewID)
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
