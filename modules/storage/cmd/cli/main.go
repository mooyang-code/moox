package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/bootstrap/metadata"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	"github.com/mooyang-code/moox/modules/storage/internal/service/datanode"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	trpc "trpc.group/trpc-go/trpc-go"
	"trpc.group/trpc-go/trpc-go/client"
)

type cliResult struct {
	Module  string      `json:"module"`
	Action  string      `json:"action"`
	Status  string      `json:"status"`
	DBPath  string      `json:"db_path"`
	Seed    string      `json:"seed_path,omitempty"`
	Summary interface{} `json:"summary,omitempty"`
}

type importSummary struct {
	Spaces          int `json:"spaces"`
	DataSources     int `json:"data_sources"`
	Subjects        int `json:"subjects"`
	SubjectSymbols  int `json:"subject_symbols"`
	Datasets        int `json:"datasets"`
	DatasetSubjects int `json:"dataset_subjects"`
	Fields          int `json:"fields"`
	Factors         int `json:"factors"`
	DatasetColumns  int `json:"dataset_columns"`
	Views           int `json:"views"`
	ViewColumns     int `json:"view_columns"`
	Devices         int `json:"devices"`
}

const storageDeployerAppID = "storage-deployer"

type metadataDeploymentClient interface {
	RegisterDataNode(context.Context, *storagepb.RegisterDataNodeReq) (*storagepb.RegisterDataNodeRsp, error)
	ListDatasets(context.Context, *storagepb.ListDatasetsReq) (*storagepb.ListDatasetsRsp, error)
	CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error)
	ActivateDataset(context.Context, *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error)
}

type dataNodeRuntimeClient interface {
	GetNodeState(context.Context, *storagepb.GetNodeStateReq) (*storagepb.GetNodeStateRsp, error)
}

type metadataDeploymentProxy struct {
	proxy   storagepb.MetadataClientProxy
	options []client.Option
}

type dataNodeRuntimeProxy struct {
	proxy   storagepb.DataNodeRuntimeClientProxy
	options []client.Option
}

func (c *metadataDeploymentProxy) RegisterDataNode(ctx context.Context, req *storagepb.RegisterDataNodeReq) (*storagepb.RegisterDataNodeRsp, error) {
	return c.proxy.RegisterDataNode(ctx, req, c.options...)
}

func (c *metadataDeploymentProxy) ListDatasets(ctx context.Context, req *storagepb.ListDatasetsReq) (*storagepb.ListDatasetsRsp, error) {
	return c.proxy.ListDatasets(ctx, req, c.options...)
}

func (c *metadataDeploymentProxy) CheckDatasetActivation(ctx context.Context, req *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	return c.proxy.CheckDatasetActivation(ctx, req, c.options...)
}

func (c *metadataDeploymentProxy) ActivateDataset(ctx context.Context, req *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	return c.proxy.ActivateDataset(ctx, req, c.options...)
}

func (c *dataNodeRuntimeProxy) GetNodeState(ctx context.Context, req *storagepb.GetNodeStateReq) (*storagepb.GetNodeStateRsp, error) {
	return c.proxy.GetNodeState(ctx, req, c.options...)
}

var newMetadataDeploymentClient = func(target string) metadataDeploymentClient {
	options := []client.Option{
		client.WithTarget(target),
		client.WithNetwork("tcp"),
		client.WithProtocol("trpc"),
	}
	return &metadataDeploymentProxy{
		proxy:   storagepb.NewMetadataClientProxy(options...),
		options: options,
	}
}

var newDataNodeRuntimeClient = func(target string) dataNodeRuntimeClient {
	options := []client.Option{
		client.WithTarget(target),
		client.WithNetwork("tcp"),
		client.WithProtocol("trpc"),
	}
	return &dataNodeRuntimeProxy{
		proxy:   storagepb.NewDataNodeRuntimeClientProxy(options...),
		options: options,
	}
}

type operationResult struct {
	Module  string      `json:"module"`
	Action  string      `json:"action"`
	Status  string      `json:"status"`
	Summary interface{} `json:"summary"`
}

type registerNodeResult struct {
	NodeID        string `json:"node_id"`
	ServiceTarget string `json:"service_target"`
	Name          string `json:"name"`
	Status        string `json:"status"`
}

type activationResult struct {
	Datasets []datasetActivationResult `json:"datasets"`
}

type datasetActivationResult struct {
	SpaceID   string                  `json:"space_id"`
	DatasetID string                  `json:"dataset_id"`
	Revision  uint64                  `json:"revision"`
	Status    string                  `json:"status"`
	Checks    []activationCheckResult `json:"checks,omitempty"`
}

type activationCheckResult struct {
	CheckID string `json:"check_id"`
	Ready   bool   `json:"ready"`
	Summary string `json:"summary"`
}

func main() {
	if err := runCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		printError(os.Stderr, err)
		os.Exit(exitCode(err))
	}
}

func runCommand(args []string, stdout io.Writer, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if len(args) == 0 {
		return fmt.Errorf("expected command: init, import-seed, register-node, activate-datasets, repair-view, force-rebuild-view, or purge-dataset-events")
	}
	switch args[0] {
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "import-seed":
		return runImportSeed(args[1:], stdout, stderr)
	case "register-node":
		return runRegisterNode(trpc.BackgroundContext(), args[1:], stdout, newMetadataDeploymentClient, newDataNodeRuntimeClient)
	case "activate-datasets":
		return runActivateDatasets(trpc.BackgroundContext(), args[1:], stdout, newMetadataDeploymentClient)
	case "repair-view":
		return runRepairView(args[1:], stdout, stderr)
	case "force-rebuild-view":
		return runForceRebuildView(args[1:], stdout, stderr)
	case "purge-dataset-events":
		return runPurgeDatasetEvents(trpc.BackgroundContext(), args[1:], stdout)
	default:
		return fmt.Errorf("unknown command %q: use init, import-seed, register-node, activate-datasets, repair-view, force-rebuild-view, or purge-dataset-events", args[0])
	}
}

type registerNodeOptions struct {
	metadataTarget string
	nodeID         string
	serviceTarget  string
	name           string
}

func runRegisterNode(ctx context.Context, args []string, stdout io.Writer, metadataFactory func(string) metadataDeploymentClient, nodeFactory func(string) dataNodeRuntimeClient) error {
	fs := flag.NewFlagSet("register-node", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := registerNodeOptions{}
	fs.StringVar(&opts.metadataTarget, "metadata-target", "", "Metadata tRPC target")
	fs.StringVar(&opts.nodeID, "node-id", "", "DataNode ID")
	fs.StringVar(&opts.serviceTarget, "service-target", "", "DataNode runtime target")
	fs.StringVar(&opts.name, "name", "", "DataNode display name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected register-node arguments: %s", strings.Join(fs.Args(), " "))
	}
	if err := validateRegisterNodeOptions(opts); err != nil {
		return err
	}
	auth, err := deploymentAuth()
	if err != nil {
		return err
	}
	nodeRsp, err := nodeFactory(opts.serviceTarget).GetNodeState(ctx, &storagepb.GetNodeStateReq{
		AuthInfo: auth,
		NodeId:   opts.nodeID,
	})
	if err != nil {
		return fmt.Errorf("DataNode readiness RPC failed")
	}
	if nodeRsp == nil {
		return fmt.Errorf("DataNode readiness returned no response")
	}
	if err := requireSuccess("DataNode readiness", nodeRsp.GetRetInfo()); err != nil {
		return err
	}
	if nodeRsp.GetNodeId() != opts.nodeID || nodeRsp.GetStatus() != "READY" {
		return fmt.Errorf("DataNode is not ready")
	}
	rsp, err := metadataFactory(opts.metadataTarget).RegisterDataNode(ctx, &storagepb.RegisterDataNodeReq{
		AuthInfo:      auth,
		NodeId:        opts.nodeID,
		ServiceTarget: opts.serviceTarget,
		InitialName:   opts.name,
	})
	if err != nil {
		return fmt.Errorf("register DataNode RPC failed")
	}
	if rsp == nil {
		return fmt.Errorf("register DataNode returned no response")
	}
	if err := requireSuccess("register DataNode", rsp.GetRetInfo()); err != nil {
		return err
	}
	node := rsp.GetNode()
	if node == nil {
		return fmt.Errorf("register DataNode returned no node")
	}
	return writeOperationResult(stdout, operationResult{
		Module: "storage",
		Action: "register-node",
		Status: "ok",
		Summary: registerNodeResult{
			NodeID:        node.GetNodeId(),
			ServiceTarget: node.GetServiceTarget(),
			Name:          node.GetName(),
			Status:        node.GetStatus(),
		},
	})
}

func validateRegisterNodeOptions(opts registerNodeOptions) error {
	for _, item := range []struct {
		name  string
		value string
	}{
		{name: "metadata-target", value: opts.metadataTarget},
		{name: "node-id", value: opts.nodeID},
		{name: "service-target", value: opts.serviceTarget},
	} {
		if strings.TrimSpace(item.value) == "" {
			return fmt.Errorf("--%s is required", item.name)
		}
	}
	return nil
}

func deploymentAuth() (*storagepb.AuthInfo, error) {
	secret := strings.TrimSpace(os.Getenv("MOOX_STORAGE_NODE_AUTH_SECRET"))
	if secret == "" {
		return nil, fmt.Errorf("MOOX_STORAGE_NODE_AUTH_SECRET is required")
	}
	return &storagepb.AuthInfo{
		AppId:  storageDeployerAppID,
		AppKey: datanode.ServiceAuthKey(secret, storageDeployerAppID),
	}, nil
}

func runActivateDatasets(ctx context.Context, args []string, stdout io.Writer, factory func(string) metadataDeploymentClient) error {
	fs := flag.NewFlagSet("activate-datasets", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var metadataTarget string
	fs.StringVar(&metadataTarget, "metadata-target", "", "Metadata tRPC target")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected activate-datasets arguments: %s", strings.Join(fs.Args(), " "))
	}
	if strings.TrimSpace(metadataTarget) == "" {
		return fmt.Errorf("--metadata-target is required")
	}
	auth, err := deploymentAuth()
	if err != nil {
		return err
	}
	client := factory(metadataTarget)
	datasets, err := listAllDatasets(ctx, client, auth)
	if err != nil {
		return err
	}
	sort.Slice(datasets, func(i, j int) bool {
		if datasets[i].GetSpaceId() != datasets[j].GetSpaceId() {
			return datasets[i].GetSpaceId() < datasets[j].GetSpaceId()
		}
		return datasets[i].GetDatasetId() < datasets[j].GetDatasetId()
	})

	result := activationResult{Datasets: make([]datasetActivationResult, 0)}
	failed := false
	for _, dataset := range datasets {
		if dataset == nil || dataset.GetStatus() != "disabled" {
			continue
		}
		item := datasetActivationResult{
			SpaceID:   dataset.GetSpaceId(),
			DatasetID: dataset.GetDatasetId(),
			Revision:  dataset.GetRevision(),
			Status:    "checking",
		}
		checkRsp, checkErr := client.CheckDatasetActivation(ctx, &storagepb.CheckDatasetActivationReq{
			AuthInfo:  auth,
			SpaceId:   item.SpaceID,
			DatasetId: item.DatasetID,
		})
		if checkErr != nil || checkRsp == nil {
			item.Status = "check_failed"
			failed = true
			result.Datasets = append(result.Datasets, item)
			continue
		}
		item.Revision = checkRsp.GetDatasetRevision()
		item.Checks = sanitizeActivationChecks(checkRsp.GetChecks())
		if err := requireSuccess("check Dataset activation", checkRsp.GetRetInfo()); err != nil {
			item.Status = "check_failed"
			failed = true
			result.Datasets = append(result.Datasets, item)
			continue
		}
		if !checkRsp.GetReady() {
			item.Status = "not_ready"
			failed = true
			result.Datasets = append(result.Datasets, item)
			continue
		}
		activateRsp, activateErr := client.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{
			AuthInfo:         auth,
			SpaceId:          item.SpaceID,
			DatasetId:        item.DatasetID,
			ExpectedRevision: item.Revision,
		})
		if activateErr != nil || activateRsp == nil {
			item.Status = "activation_failed"
			failed = true
			result.Datasets = append(result.Datasets, item)
			continue
		}
		if err := requireSuccess("activate Dataset", activateRsp.GetRetInfo()); err != nil {
			if activateRsp.GetRetInfo() != nil && activateRsp.GetRetInfo().GetCode() == storagepb.ErrorCode_CONFLICT {
				item.Status = "conflict"
			} else {
				item.Status = "activation_failed"
			}
			failed = true
			result.Datasets = append(result.Datasets, item)
			continue
		}
		item.Status = "active"
		if activateRsp.GetDataset() != nil {
			item.Revision = activateRsp.GetDataset().GetRevision()
		}
		result.Datasets = append(result.Datasets, item)
	}

	status := "ok"
	if failed {
		status = "failed"
	}
	if err := writeOperationResult(stdout, operationResult{
		Module:  "storage",
		Action:  "activate-datasets",
		Status:  status,
		Summary: result,
	}); err != nil {
		return err
	}
	if failed {
		return fmt.Errorf("one or more Datasets could not be activated")
	}
	return nil
}

func listAllDatasets(ctx context.Context, client metadataDeploymentClient, auth *storagepb.AuthInfo) ([]*storagepb.Dataset, error) {
	const pageSize uint32 = 100
	const maxPages uint32 = 10000
	all := make([]*storagepb.Dataset, 0)
	for page := uint32(1); page <= maxPages; page++ {
		rsp, err := client.ListDatasets(ctx, &storagepb.ListDatasetsReq{
			AuthInfo: auth,
			Page:     &storagepb.Page{Page: page, Size: pageSize},
		})
		if err != nil {
			return nil, fmt.Errorf("list Datasets RPC failed")
		}
		if rsp == nil {
			return nil, fmt.Errorf("list Datasets returned no response")
		}
		if err := requireSuccess("list Datasets", rsp.GetRetInfo()); err != nil {
			return nil, err
		}
		all = append(all, rsp.GetDatasets()...)
		pageResult := rsp.GetPageResult()
		if pageResult == nil || !pageResult.GetHasMore() {
			return all, nil
		}
	}
	return nil, fmt.Errorf("list Datasets exceeded %d pages", maxPages)
}

func sanitizeActivationChecks(checks []*storagepb.DatasetActivationCheck) []activationCheckResult {
	result := make([]activationCheckResult, 0, len(checks))
	for _, check := range checks {
		if check == nil {
			continue
		}
		result = append(result, activationCheckResult{
			CheckID: check.GetCheckId(),
			Ready:   check.GetReady(),
			Summary: check.GetSummary(),
		})
	}
	return result
}

func requireSuccess(action string, ret interface{ GetCode() storagepb.ErrorCode }) error {
	if ret == nil || ret.GetCode() != storagepb.ErrorCode_SUCCESS {
		if ret == nil {
			return fmt.Errorf("%s returned no status", action)
		}
		return fmt.Errorf("%s returned %s", action, ret.GetCode().String())
	}
	return nil
}

func writeOperationResult(stdout io.Writer, result operationResult) error {
	return json.NewEncoder(stdout).Encode(result)
}

func runInit(args []string, stdout io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := commandOptions{}
	registerCommonFlags(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected init arguments: %s", strings.Join(fs.Args(), " "))
	}
	storage, err := loadStorage(opts.storageConf)
	if err != nil {
		return err
	}
	schemaPath := resolveSchemaPath(opts.schemaPath)
	if err := metadata.InitSchema(trpc.BackgroundContext(), metadata.SchemaOptions{
		Storage:    storage,
		SchemaPath: schemaPath,
	}); err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(cliResult{
		Module: "storage",
		Action: "init",
		Status: "ok",
		DBPath: metadataDBPath(storage),
	})
}

func runImportSeed(args []string, stdout io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("import-seed", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := commandOptions{}
	registerCommonFlags(fs, &opts)
	fs.StringVar(&opts.seedPath, "seed", "", "metadata seed yaml path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected import-seed arguments: %s", strings.Join(fs.Args(), " "))
	}
	storage, err := loadStorage(opts.storageConf)
	if err != nil {
		return err
	}
	seedPath := resolveSeedPath(opts.seedPath, opts.storageConf)
	result, err := metadata.ImportSeed(trpc.BackgroundContext(), metadata.SeedOptions{
		Storage:    storage,
		SchemaPath: resolveSchemaPath(opts.schemaPath),
		SeedPath:   seedPath,
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(cliResult{
		Module: "storage",
		Action: "import-seed",
		Status: "ok",
		DBPath: metadataDBPath(storage),
		Seed:   seedPath,
		Summary: importSummary{
			Spaces:          result.Spaces,
			DataSources:     result.DataSources,
			Subjects:        result.Subjects,
			SubjectSymbols:  result.SubjectSymbols,
			Datasets:        result.Datasets,
			DatasetSubjects: result.DatasetSubjects,
			Fields:          result.Fields,
			Factors:         result.Factors,
			DatasetColumns:  result.DatasetColumns,
			Views:           result.Views,
			ViewColumns:     result.ViewColumns,
			Devices:         result.Devices,
		},
	})
}

type commandOptions struct {
	storageConf string
	schemaPath  string
	seedPath    string
}

func registerCommonFlags(fs *flag.FlagSet, opts *commandOptions) {
	fs.StringVar(&opts.storageConf, "storage-conf", defaultStorageConfigPath(), "storage business config path")
	fs.StringVar(&opts.schemaPath, "schema-path", "", "metadata schema sql path")
}

func loadStorage(configPath string) (storageconfig.StorageConfig, error) {
	var cfg storageconfig.RuntimeConfig
	if configPath != "" {
		dir := filepath.Dir(configPath)
		file := filepath.Base(configPath)
		if err := storageconfig.NewConfigLoader(dir).LoadConfigWithDefaults(file, &cfg, cfg.ApplyDefaults); err != nil {
			return cfg.Storage, fmt.Errorf("load storage config %s: %w", configPath, err)
		}
	} else {
		cfg.ApplyDefaults()
	}
	if root := os.Getenv("MOOX_STORAGE_HOME"); root != "" {
		cfg.Storage.ApplyHomeRoot(root)
	}
	return cfg.Storage, nil
}

func metadataDBPath(storage storageconfig.StorageConfig) string {
	root := storage.Root
	if root == "" {
		root = "var/storage"
	}
	if storage.Metadata.Path != "" {
		return storage.Metadata.Path
	}
	return filepath.Join(root, "metadata", "storage_metadata.db")
}

func defaultStorageConfigPath() string {
	if path := os.Getenv("MOOX_STORAGE_CONFIG"); path != "" {
		return path
	}
	if path := os.Getenv("STORAGE_APP_CONFIG"); path != "" {
		return path
	}
	if dir := os.Getenv("STORAGE_CONFIG_PATH"); dir != "" {
		return filepath.Join(dir, "storage.yaml")
	}
	return filepath.Join("config", "storage.yaml")
}

func resolveSchemaPath(path string) string {
	if path != "" {
		return path
	}
	if path := os.Getenv("STORAGE_SCHEMA_FILE"); path != "" {
		return path
	}
	candidates := []string{
		filepath.Join("schema", "metadata.sql"),
		filepath.Join("modules", "storage", "schema", "metadata.sql"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return candidates[0]
}

func resolveSeedPath(path string, _ string) string {
	if path != "" {
		return path
	}
	if path := os.Getenv("STORAGE_SEED_FILE"); path != "" {
		return path
	}
	return ""
}

func printError(stderr io.Writer, err error) {
	if stderr == nil {
		stderr = io.Discard
	}
	_ = json.NewEncoder(stderr).Encode(map[string]string{
		"error":   "storage_cli_failed",
		"message": err.Error(),
	})
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	return 1
}
