package command

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/spf13/cobra"
	"trpc.group/trpc-go/trpc-go/client"
)

const (
	storageMetadataRemoteAddress = "127.0.0.1:20200"
	storagePrimaryRemoteAddress  = "127.0.0.1:20101"
	adminSpaceRemoteAddress      = "127.0.0.1:11107"
	storageBrowserRemoteAddress  = "127.0.0.1:9527"
	storageAuthFile              = "$HOME/moox/storage/secrets/storage-node-auth.env"
	storagePrimaryAuthFile       = "$HOME/moox/storage/secrets/gateway-storage-primary.key"
	storageRemoteProvenanceFile  = "$HOME/moox/storage/build-provenance.json"
	storageLocalProvenanceFile   = "release/deploy-stage/moox/build-provenance.json"
	storageReleaseManifestFile   = "artifacts/storage-datanode-release-sha256.txt"
	storageE2ESpec               = "tests/storage-datanode-management.remote.e2e.spec.ts"
	storageDeploymentNodeID      = "storage-node-0"
)

type storageVerifyResult struct {
	Status             string                      `json:"status"`
	Commit             string                      `json:"commit"`
	Components         map[string]storageComponent `json:"components"`
	BinaryHashes       map[string]string           `json:"binary_hashes"`
	SchemaVersion      int                         `json:"schema_version"`
	DataNode           storageDataNodeIdentity     `json:"data_node"`
	NodeCount          int                         `json:"node_count"`
	DatasetCount       int                         `json:"dataset_count"`
	RouteRPCRegistered bool                        `json:"route_rpc_registered"`
}

type storageComponent struct {
	Status string `json:"status"`
}

type storageDataNodeIdentity struct {
	NodeID string `json:"node_id"`
	Status string `json:"status"`
}

type storageE2EResult struct {
	Status     string   `json:"status"`
	Namespace  string   `json:"namespace"`
	Assertions []string `json:"assertions"`
	Skipped    []string `json:"skipped,omitempty"`
	Cleanup    string   `json:"cleanup"`
}

type storageBrowserResult struct {
	Status  string `json:"status"`
	Desktop string `json:"desktop"`
	Mobile  string `json:"mobile"`
}

type storageBrowserFixture struct {
	Namespace   string `json:"namespace"`
	SpaceID     string `json:"space_id"`
	SourceID    string `json:"data_source_id"`
	DatasetID   string `json:"dataset_id"`
	DatasetName string `json:"dataset_name"`
}

type storageBuildProvenance struct {
	SchemaVersion int               `json:"schema_version"`
	Commit        string            `json:"commit"`
	Dirty         bool              `json:"dirty"`
	BinaryHashes  map[string]string `json:"binary_hashes"`
}

type storageReleaseArtifact struct {
	SchemaVersion int
	Commit        string
	Archive       string
	ArchiveSHA256 string
	BinaryHashes  map[string]string
}

type storageMetadataAPI interface {
	CreateSpace(context.Context, *storagepb.CreateSpaceReq) (*storagepb.CreateSpaceRsp, error)
	UpdateSpace(context.Context, *storagepb.UpdateSpaceReq) (*storagepb.UpdateSpaceRsp, error)
	DeleteSpace(context.Context, *storagepb.DeleteSpaceReq) (*storagepb.DeleteSpaceRsp, error)
	CreateDataSource(context.Context, *storagepb.CreateDataSourceReq) (*storagepb.CreateDataSourceRsp, error)
	UpdateDataSource(context.Context, *storagepb.UpdateDataSourceReq) (*storagepb.UpdateDataSourceRsp, error)
	DeleteDataSource(context.Context, *storagepb.DeleteDataSourceReq) (*storagepb.DeleteDataSourceRsp, error)
	CreateDataset(context.Context, *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error)
	UpdateDataset(context.Context, *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error)
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error)
	UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error)
	RegisterDataNode(context.Context, *storagepb.RegisterDataNodeReq) (*storagepb.RegisterDataNodeRsp, error)
	UpdateDataNode(context.Context, *storagepb.UpdateDataNodeReq) (*storagepb.UpdateDataNodeRsp, error)
	RebindDatasetDataNode(context.Context, *storagepb.RebindDatasetDataNodeReq) (*storagepb.RebindDatasetDataNodeRsp, error)
	DeleteDataNode(context.Context, *storagepb.DeleteDataNodeReq) (*storagepb.DeleteDataNodeRsp, error)
	ListDataNodes(context.Context, *storagepb.ListDataNodesReq) (*storagepb.ListDataNodesRsp, error)
	CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error)
	ActivateDataset(context.Context, *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error)
}

type storageAdminSpaceAPI interface {
	CreateSpace(context.Context, *adminpb.CreateSpaceReq) (*adminpb.CreateSpaceRsp, error)
	UpdateSpace(context.Context, *adminpb.UpdateSpaceReq) (*adminpb.UpdateSpaceRsp, error)
}

type storageAdminSpaceProxy struct {
	proxy   adminpb.SpaceMgrClientProxy
	options []client.Option
}

func (c *storageAdminSpaceProxy) CreateSpace(ctx context.Context, req *adminpb.CreateSpaceReq) (*adminpb.CreateSpaceRsp, error) {
	return c.proxy.CreateSpace(ctx, req, c.options...)
}

func (c *storageAdminSpaceProxy) UpdateSpace(ctx context.Context, req *adminpb.UpdateSpaceReq) (*adminpb.UpdateSpaceRsp, error) {
	return c.proxy.UpdateSpace(ctx, req, c.options...)
}

type storagePrimaryAPI interface {
	UpsertFields(context.Context, *storagepb.PrimaryUpsertFieldsReq) (*storagepb.PrimaryUpsertFieldsRsp, error)
	ReadFields(context.Context, *storagepb.PrimaryReadFieldsReq) (*storagepb.PrimaryReadFieldsRsp, error)
}

type storageRuntimeAPI interface {
	GetNodeState(context.Context, *storagepb.GetNodeStateReq) (*storagepb.GetNodeStateRsp, error)
}

type storageMetadataProxy struct {
	proxy   storagepb.MetadataClientProxy
	options []client.Option
}

func (c *storageMetadataProxy) CreateSpace(ctx context.Context, req *storagepb.CreateSpaceReq) (*storagepb.CreateSpaceRsp, error) {
	return c.proxy.CreateSpace(ctx, req, c.options...)
}

func (c *storageMetadataProxy) UpdateSpace(ctx context.Context, req *storagepb.UpdateSpaceReq) (*storagepb.UpdateSpaceRsp, error) {
	return c.proxy.UpdateSpace(ctx, req, c.options...)
}

func (c *storageMetadataProxy) DeleteSpace(ctx context.Context, req *storagepb.DeleteSpaceReq) (*storagepb.DeleteSpaceRsp, error) {
	return c.proxy.DeleteSpace(ctx, req, c.options...)
}

func (c *storageMetadataProxy) CreateDataSource(ctx context.Context, req *storagepb.CreateDataSourceReq) (*storagepb.CreateDataSourceRsp, error) {
	return c.proxy.CreateDataSource(ctx, req, c.options...)
}

func (c *storageMetadataProxy) UpdateDataSource(ctx context.Context, req *storagepb.UpdateDataSourceReq) (*storagepb.UpdateDataSourceRsp, error) {
	return c.proxy.UpdateDataSource(ctx, req, c.options...)
}

func (c *storageMetadataProxy) DeleteDataSource(ctx context.Context, req *storagepb.DeleteDataSourceReq) (*storagepb.DeleteDataSourceRsp, error) {
	return c.proxy.DeleteDataSource(ctx, req, c.options...)
}

func (c *storageMetadataProxy) CreateDataset(ctx context.Context, req *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error) {
	return c.proxy.CreateDataset(ctx, req, c.options...)
}

func (c *storageMetadataProxy) UpdateDataset(ctx context.Context, req *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error) {
	return c.proxy.UpdateDataset(ctx, req, c.options...)
}

func (c *storageMetadataProxy) DeleteDataset(ctx context.Context, req *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error) {
	return c.proxy.DeleteDataset(ctx, req, c.options...)
}

func (c *storageMetadataProxy) UpsertDatasetColumn(ctx context.Context, req *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error) {
	return c.proxy.UpsertDatasetColumn(ctx, req, c.options...)
}

func (c *storageMetadataProxy) RegisterDataNode(ctx context.Context, req *storagepb.RegisterDataNodeReq) (*storagepb.RegisterDataNodeRsp, error) {
	return c.proxy.RegisterDataNode(ctx, req, c.options...)
}

func (c *storageMetadataProxy) UpdateDataNode(ctx context.Context, req *storagepb.UpdateDataNodeReq) (*storagepb.UpdateDataNodeRsp, error) {
	return c.proxy.UpdateDataNode(ctx, req, c.options...)
}

func (c *storageMetadataProxy) RebindDatasetDataNode(ctx context.Context, req *storagepb.RebindDatasetDataNodeReq) (*storagepb.RebindDatasetDataNodeRsp, error) {
	return c.proxy.RebindDatasetDataNode(ctx, req, c.options...)
}

func (c *storageMetadataProxy) DeleteDataNode(ctx context.Context, req *storagepb.DeleteDataNodeReq) (*storagepb.DeleteDataNodeRsp, error) {
	return c.proxy.DeleteDataNode(ctx, req, c.options...)
}

func (c *storageMetadataProxy) ListDataNodes(ctx context.Context, req *storagepb.ListDataNodesReq) (*storagepb.ListDataNodesRsp, error) {
	return c.proxy.ListDataNodes(ctx, req, c.options...)
}

func (c *storageMetadataProxy) CheckDatasetActivation(ctx context.Context, req *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error) {
	return c.proxy.CheckDatasetActivation(ctx, req, c.options...)
}

func (c *storageMetadataProxy) ActivateDataset(ctx context.Context, req *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error) {
	return c.proxy.ActivateDataset(ctx, req, c.options...)
}

type storageRuntimeProxy struct {
	proxy   storagepb.DataNodeRuntimeClientProxy
	options []client.Option
}

type storagePrimaryProxy struct {
	proxy   storagepb.PrimaryStoreClientProxy
	options []client.Option
}

func (c *storagePrimaryProxy) UpsertFields(ctx context.Context, req *storagepb.PrimaryUpsertFieldsReq) (*storagepb.PrimaryUpsertFieldsRsp, error) {
	return c.proxy.UpsertFields(ctx, req, c.options...)
}

func (c *storagePrimaryProxy) ReadFields(ctx context.Context, req *storagepb.PrimaryReadFieldsReq) (*storagepb.PrimaryReadFieldsRsp, error) {
	return c.proxy.ReadFields(ctx, req, c.options...)
}

func (c *storageRuntimeProxy) GetNodeState(ctx context.Context, req *storagepb.GetNodeStateReq) (*storagepb.GetNodeStateRsp, error) {
	return c.proxy.GetNodeState(ctx, req, c.options...)
}

type remoteStorageSession struct {
	transport       setupssh.Client
	metadata        storageMetadataAPI
	primary         storagePrimaryAPI
	auth            *storagepb.AuthInfo
	nodeAuth        *storagepb.AuthInfo
	primaryAuth     *storagepb.AuthInfo
	cancel          context.CancelFunc
	listener        net.Listener
	primaryCancel   context.CancelFunc
	primaryListener net.Listener
}

func (s *remoteStorageSession) Close() {
	if s == nil {
		return
	}
	if s.listener != nil {
		_ = s.listener.Close()
	}
	if s.primaryListener != nil {
		_ = s.primaryListener.Close()
	}
	if s.cancel != nil {
		s.cancel()
	}
	if s.primaryCancel != nil {
		s.primaryCancel()
	}
}

func newRemoteStorageSession(ctx context.Context, transport setupssh.Client, secret, primarySecret string) (*remoteStorageSession, error) {
	if transport == nil || strings.TrimSpace(secret) == "" || strings.TrimSpace(primarySecret) == "" {
		return nil, errors.New("storage_verification_unavailable")
	}
	forwardContext, cancel := context.WithCancel(ctx)
	listener, err := transport.ForwardLocal(forwardContext, storageMetadataRemoteAddress)
	if err != nil {
		cancel()
		return nil, errors.New("storage_not_reachable")
	}
	primaryForwardContext, primaryCancel := context.WithCancel(ctx)
	primaryListener, err := transport.ForwardLocal(primaryForwardContext, storagePrimaryRemoteAddress)
	if err != nil {
		_ = listener.Close()
		cancel()
		primaryCancel()
		return nil, errors.New("storage_primary_not_reachable")
	}
	target := "ip://" + listener.Addr().String()
	options := []client.Option{client.WithTarget(target), client.WithNetwork("tcp"), client.WithProtocol("http")}
	primaryOptions := []client.Option{client.WithTarget("ip://" + primaryListener.Addr().String()), client.WithNetwork("tcp"), client.WithProtocol("trpc")}
	return &remoteStorageSession{
		transport:       transport,
		metadata:        &storageMetadataProxy{proxy: storagepb.NewMetadataClientProxy(options...), options: options},
		primary:         &storagePrimaryProxy{proxy: storagepb.NewPrimaryStoreClientProxy(primaryOptions...), options: primaryOptions},
		auth:            &storagepb.AuthInfo{AppId: "storage-metadata", AppKey: security.HMACSHA256Hex(secret, []byte("storage-metadata"))},
		nodeAuth:        &storagepb.AuthInfo{AppId: "storage-deployer", AppKey: security.HMACSHA256Hex(secret, []byte("storage-deployer"))},
		primaryAuth:     &storagepb.AuthInfo{AppId: "storage-e2e", AppKey: security.HMACSHA256Hex(primarySecret, []byte("storage-e2e"))},
		cancel:          cancel,
		listener:        listener,
		primaryCancel:   primaryCancel,
		primaryListener: primaryListener,
	}, nil
}

func (s *remoteStorageSession) runtime(ctx context.Context, node *storagepb.DataNode) (storageRuntimeAPI, func(), error) {
	if s == nil || node == nil {
		return nil, func() {}, errors.New("storage_node_unavailable")
	}
	address, err := storageTargetAddress(node.GetServiceTarget())
	if err != nil {
		return nil, func() {}, errors.New("storage_node_target_invalid")
	}
	forwardContext, cancel := context.WithCancel(ctx)
	listener, err := s.transport.ForwardLocal(forwardContext, address)
	if err != nil {
		cancel()
		return nil, func() {}, errors.New("storage_node_unreachable")
	}
	target := "ip://" + listener.Addr().String()
	options := []client.Option{client.WithTarget(target), client.WithNetwork("tcp"), client.WithProtocol("trpc")}
	closeFn := func() {
		_ = listener.Close()
		cancel()
	}
	return &storageRuntimeProxy{proxy: storagepb.NewDataNodeRuntimeClientProxy(options...), options: options}, closeFn, nil
}

func newSetupVerifyStorageCommand(deps setupDeps) *cobra.Command {
	var file, host string
	cmd := &cobra.Command{Use: "verify-storage", Short: "验证远端 Storage 健康、Schema 和 DataNode 身份", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		result, err := deps.verifyStorage(cmd.Context(), snapshot, host)
		if err != nil {
			return err
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, result)
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&host, "host", "", "Storage 目标主机名称")
	_ = cmd.MarkFlagRequired("host")
	return cmd
}

func newSetupE2EStorageCommand(deps setupDeps) *cobra.Command {
	var file, host, namespace string
	cmd := &cobra.Command{Use: "e2e-storage", Short: "运行隔离的 Storage 元数据生命周期验证", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		result, err := deps.e2eStorage(cmd.Context(), snapshot, host, namespace)
		if err != nil {
			return err
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, result)
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&host, "host", "", "Storage 目标主机名称")
	cmd.Flags().StringVar(&namespace, "namespace", "", "隔离 E2E 数据命名空间")
	_ = cmd.MarkFlagRequired("host")
	_ = cmd.MarkFlagRequired("namespace")
	return cmd
}

func newSetupBrowserE2EStorageCommand(deps setupDeps) *cobra.Command {
	var file, host, repoRoot string
	cmd := &cobra.Command{Use: "browser-e2e-storage", Short: "运行远端 Storage 管理台浏览器验证", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		snapshot, err := deps.load(file)
		if err != nil {
			return err
		}
		defer clearSetupSecrets(snapshot)
		result, err := deps.browserE2EStorage(cmd.Context(), snapshot, host, repoRoot)
		if err != nil {
			return err
		}
		if err := snapshot.VerifyUnchanged(); err != nil {
			return fmt.Errorf("config_changed")
		}
		return writeSetupJSON(cmd, result)
	}}
	cmd.Flags().StringVar(&file, "file", defaultSetupFile, "初始化配置文件")
	cmd.Flags().StringVar(&host, "host", "", "Storage/Admin 目标主机名称")
	cmd.Flags().StringVar(&repoRoot, "repo-root", "", "仓库根目录")
	_ = cmd.MarkFlagRequired("host")
	_ = cmd.MarkFlagRequired("repo-root")
	return cmd
}

func defaultSetupVerifyStorage(ctx context.Context, snapshot *setupconfig.Snapshot, name string) (storageVerifyResult, error) {
	_, transport, session, root, err := openRemoteStorage(ctx, snapshot, name)
	if err != nil {
		return storageVerifyResult{}, err
	}
	defer transport.Close()
	defer session.Close()
	return verifyRemoteStorage(ctx, transport, session, root)
}

func defaultSetupE2EStorage(ctx context.Context, snapshot *setupconfig.Snapshot, name, namespace string) (storageE2EResult, error) {
	_, transport, session, _, err := openRemoteStorage(ctx, snapshot, name)
	if err != nil {
		return storageE2EResult{}, err
	}
	defer transport.Close()
	defer session.Close()
	return runStorageLifecycle(ctx, session, namespace)
}

func openRemoteStorage(ctx context.Context, snapshot *setupconfig.Snapshot, name string) (setupconfig.Host, setupssh.Client, *remoteStorageSession, string, error) {
	if snapshot == nil {
		return setupconfig.Host{}, nil, nil, "", errors.New("storage_verification_invalid")
	}
	host, err := findSetupHost(snapshot.Manifest, name)
	if err != nil {
		return setupconfig.Host{}, nil, nil, "", err
	}
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return setupconfig.Host{}, nil, nil, "", err
	}
	secret, err := readRemoteStorageSecret(ctx, transport)
	if err != nil {
		_ = transport.Close()
		return setupconfig.Host{}, nil, nil, "", err
	}
	primarySecret, err := readRemoteStoragePrimarySecret(ctx, transport)
	if err != nil {
		_ = transport.Close()
		return setupconfig.Host{}, nil, nil, "", err
	}
	session, err := newRemoteStorageSession(ctx, transport, secret, primarySecret)
	if err != nil {
		_ = transport.Close()
		return setupconfig.Host{}, nil, nil, "", err
	}
	root, err := os.Getwd()
	if err != nil {
		session.Close()
		_ = transport.Close()
		return setupconfig.Host{}, nil, nil, "", errors.New("storage_verification_invalid")
	}
	return host, transport, session, root, nil
}

func readRemoteStorageSecret(ctx context.Context, transport setupssh.Client) (string, error) {
	result, err := transport.Run(ctx, []string{"sh", "-lc", `set -eu
value=$(sed -n 's/^MOOX_STORAGE_NODE_AUTH_SECRET=//p' "` + storageAuthFile + `" | head -n 1)
test -n "$value"
case "$value" in *[!A-Za-z0-9._-]*) exit 1 ;; esac
printf '%s' "$value"`}, nil)
	if err != nil || strings.TrimSpace(result.Stdout) == "" || strings.ContainsAny(result.Stdout, "\r\n") {
		return "", errors.New("storage_verification_auth_unavailable")
	}
	return strings.TrimSpace(result.Stdout), nil
}

func readRemoteStoragePrimarySecret(ctx context.Context, transport setupssh.Client) (string, error) {
	result, err := transport.Run(ctx, []string{"sh", "-lc", `set -eu
value=$(sed -n '1p' "` + storagePrimaryAuthFile + `" | tr -d '\r\n')
test -n "$value"
case "$value" in *[!A-Za-z0-9._-]*) exit 1 ;; esac
printf '%s' "$value"`}, nil)
	if err != nil || strings.TrimSpace(result.Stdout) == "" || strings.ContainsAny(result.Stdout, "\r\n") {
		return "", errors.New("storage_primary_auth_unavailable")
	}
	return strings.TrimSpace(result.Stdout), nil
}

func verifyRemoteStorage(ctx context.Context, transport setupssh.Client, session *remoteStorageSession, root string) (storageVerifyResult, error) {
	components, err := readStorageComponents(ctx, transport)
	if err != nil {
		return storageVerifyResult{}, err
	}
	currentCommit, err := readCurrentGitCommit(root)
	if err != nil {
		return storageVerifyResult{}, err
	}
	expectedArtifact, err := readLocalStorageReleaseArtifact(root, currentCommit)
	if err != nil {
		return storageVerifyResult{}, err
	}
	localProvenance, err := readLocalStorageBuildProvenance(root)
	if err != nil {
		return storageVerifyResult{}, err
	}
	remoteProvenance, err := readRemoteStorageBuildProvenance(ctx, transport)
	if err != nil {
		return storageVerifyResult{}, err
	}
	hashes, err := readStorageBinaryHashes(ctx, transport)
	if err != nil {
		return storageVerifyResult{}, err
	}
	if err := validateStorageReleaseArtifact(expectedArtifact, localProvenance, remoteProvenance, hashes); err != nil {
		return storageVerifyResult{}, err
	}
	routeRPCRegistered, err := readRemoteRouteRPCRegistered(ctx, transport)
	if err != nil {
		return storageVerifyResult{}, err
	}
	if routeRPCRegistered {
		return storageVerifyResult{}, errors.New("storage_route_rpc_registered")
	}
	schemaVersion, err := readStorageSchemaVersion(ctx, transport)
	if err != nil {
		return storageVerifyResult{}, err
	}
	items, err := listAllStorageDataNodes(ctx, session.metadata, session.auth)
	if err != nil {
		return storageVerifyResult{}, err
	}
	var datasetCount int
	var selected *storagepb.DataNode
	for _, item := range items {
		if item == nil {
			continue
		}
		datasetCount += len(item.GetDatasets())
	}
	selected, err = selectDeploymentDataNode(items)
	if err != nil {
		return storageVerifyResult{}, err
	}
	runtime, closeRuntime, err := session.runtime(ctx, selected)
	if err != nil {
		return storageVerifyResult{}, errors.New("storage_verification_failed")
	}
	defer closeRuntime()
	state, err := runtime.GetNodeState(ctx, &storagepb.GetNodeStateReq{AuthInfo: session.auth, NodeId: selected.GetNodeId()})
	if err != nil || state == nil || state.GetRetInfo() == nil || state.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || state.GetNodeId() != selected.GetNodeId() || state.GetStatus() != "READY" {
		return storageVerifyResult{}, errors.New("storage_verification_failed")
	}
	return storageVerifyResult{
		Status:             "passed",
		Commit:             remoteProvenance.Commit,
		Components:         components,
		BinaryHashes:       hashes,
		SchemaVersion:      schemaVersion,
		DataNode:           storageDataNodeIdentity{NodeID: state.GetNodeId(), Status: state.GetStatus()},
		NodeCount:          len(items),
		DatasetCount:       datasetCount,
		RouteRPCRegistered: routeRPCRegistered,
	}, nil
}

func readLocalStorageBuildProvenance(root string) (storageBuildProvenance, error) {
	if strings.TrimSpace(root) == "" {
		return storageBuildProvenance{}, errors.New("storage_provenance_unavailable")
	}
	raw, err := os.ReadFile(filepath.Join(root, storageLocalProvenanceFile))
	if err != nil {
		return storageBuildProvenance{}, errors.New("storage_provenance_unavailable")
	}
	return decodeStorageBuildProvenance(raw)
}

func readLocalStorageReleaseArtifact(root, currentCommit string) (storageBuildProvenance, error) {
	raw, err := os.ReadFile(filepath.Join(root, storageReleaseManifestFile))
	if err != nil {
		return storageBuildProvenance{}, errors.New("storage_release_artifact_unavailable")
	}
	artifact, err := decodeStorageReleaseArtifact(raw)
	if err != nil || artifact.Commit != strings.ToLower(currentCommit) {
		return storageBuildProvenance{}, errors.New("storage_release_artifact_stale")
	}
	archivePath := filepath.Join(root, filepath.Clean(artifact.Archive))
	rootPath, _ := filepath.Abs(root)
	absArchive, _ := filepath.Abs(archivePath)
	if rootPath == "" || absArchive == "" || !strings.HasPrefix(absArchive, rootPath+string(os.PathSeparator)) {
		return storageBuildProvenance{}, errors.New("storage_release_artifact_invalid")
	}
	digest, err := sha256File(absArchive)
	if err != nil || digest != artifact.ArchiveSHA256 {
		return storageBuildProvenance{}, errors.New("storage_release_artifact_mismatch")
	}
	return storageBuildProvenance{SchemaVersion: artifact.SchemaVersion, Commit: artifact.Commit, Dirty: false, BinaryHashes: artifact.BinaryHashes}, nil
}

func decodeStorageReleaseArtifact(raw []byte) (storageReleaseArtifact, error) {
	artifact := storageReleaseArtifact{BinaryHashes: make(map[string]string)}
	for _, line := range strings.Split(string(raw), "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "schema_version":
			artifact.SchemaVersion, _ = strconv.Atoi(parts[1])
		case "commit":
			artifact.Commit = strings.ToLower(parts[1])
		case "archive":
			artifact.Archive = parts[1]
		case "archive_sha256":
			artifact.ArchiveSHA256 = strings.ToLower(parts[1])
		case "moox-storage-primary", "moox-storage-node", "moox-storage-view":
			artifact.BinaryHashes[parts[0]] = strings.ToLower(parts[1])
		}
	}
	if artifact.SchemaVersion != 1 || !validStorageCommit(artifact.Commit) || artifact.Archive == "" || !validStorageSHA256(artifact.ArchiveSHA256) {
		return storageReleaseArtifact{}, errors.New("storage_release_artifact_invalid")
	}
	for _, name := range []string{"moox-storage-primary", "moox-storage-node", "moox-storage-view"} {
		if !validStorageSHA256(artifact.BinaryHashes[name]) {
			return storageReleaseArtifact{}, errors.New("storage_release_artifact_invalid")
		}
	}
	return artifact, nil
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func readCurrentGitCommit(root string) (string, error) {
	if strings.TrimSpace(root) == "" {
		return "", errors.New("storage_provenance_unavailable")
	}
	output, err := exec.Command("git", "-C", root, "rev-parse", "HEAD").Output()
	commit := strings.TrimSpace(string(output))
	if err != nil || !validStorageCommit(commit) {
		return "", errors.New("storage_provenance_unavailable")
	}
	return strings.ToLower(commit), nil
}

func selectDeploymentDataNode(items []*storagepb.DataNodeListItem) (*storagepb.DataNode, error) {
	for _, item := range items {
		if item == nil || item.GetNode() == nil || item.GetNode().GetNodeId() != storageDeploymentNodeID {
			continue
		}
		node := item.GetNode()
		if node.GetStatus() != "active" || strings.TrimSpace(node.GetServiceTarget()) == "" {
			return nil, errors.New("storage_deployment_node_unavailable")
		}
		return node, nil
	}
	return nil, errors.New("storage_deployment_node_missing")
}

func readRemoteStorageBuildProvenance(ctx context.Context, transport setupssh.Client) (storageBuildProvenance, error) {
	result, err := transport.Run(ctx, []string{"sh", "-lc", `set -eu
test -f "` + storageRemoteProvenanceFile + `"
cat "` + storageRemoteProvenanceFile + `"`}, nil)
	if err != nil {
		return storageBuildProvenance{}, errors.New("storage_provenance_unavailable")
	}
	return decodeStorageBuildProvenance([]byte(result.Stdout))
}

func decodeStorageBuildProvenance(raw []byte) (storageBuildProvenance, error) {
	var provenance storageBuildProvenance
	if err := json.Unmarshal(raw, &provenance); err != nil {
		return storageBuildProvenance{}, errors.New("storage_provenance_unavailable")
	}
	return provenance, nil
}

func validateStorageBuildProvenance(local, remote storageBuildProvenance, actual map[string]string) error {
	if local.SchemaVersion != 1 || remote.SchemaVersion != 1 || local.Dirty || remote.Dirty || !validStorageCommit(local.Commit) || local.Commit != remote.Commit {
		return errors.New("storage_provenance_mismatch")
	}
	for _, name := range []string{"moox-storage-primary", "moox-storage-node", "moox-storage-view"} {
		localHash := strings.ToLower(strings.TrimSpace(local.BinaryHashes[name]))
		remoteHash := strings.ToLower(strings.TrimSpace(remote.BinaryHashes[name]))
		actualHash := strings.ToLower(strings.TrimSpace(actual[name]))
		if !validStorageSHA256(localHash) || localHash != remoteHash || localHash != actualHash {
			return errors.New("storage_provenance_mismatch")
		}
	}
	return nil
}

func validateStorageReleaseArtifact(expected, local, remote storageBuildProvenance, actual map[string]string) error {
	if expected.SchemaVersion != 1 || expected.Dirty || !validStorageCommit(expected.Commit) || local.Commit != expected.Commit {
		return errors.New("storage_provenance_mismatch")
	}
	if err := validateStorageBuildProvenance(expected, remote, actual); err != nil {
		return err
	}
	for _, name := range []string{"moox-storage-primary", "moox-storage-node", "moox-storage-view"} {
		if strings.ToLower(local.BinaryHashes[name]) != strings.ToLower(expected.BinaryHashes[name]) {
			return errors.New("storage_provenance_mismatch")
		}
	}
	return nil
}

func validStorageCommit(value string) bool {
	value = strings.TrimSpace(value)
	return len(value) == 40 && validStorageHex(value)
}

func validStorageSHA256(value string) bool {
	return len(value) == 64 && validStorageHex(value)
}

func validStorageHex(value string) bool {
	_, err := hex.DecodeString(value)
	return err == nil
}

func readRemoteRouteRPCRegistered(ctx context.Context, transport setupssh.Client) (bool, error) {
	result, err := transport.Run(ctx, []string{"sh", "-lc", `set -eu
body=$(mktemp)
trap 'rm -f "$body"' EXIT
status=$(curl -sS -o "$body" -w '%{http_code}' -X POST -H 'Content-Type: application/json' \
  --data '{}' http://127.0.0.1:20200/trpc.moox.storage.Metadata/ListStorageRoutes || true)
case "$status" in
  2??) printf registered ;;
  404) printf absent ;;
  *)
    if grep -Eiq 'method.*(not found|unknown)|not implemented|no such method' "$body"; then
      printf absent
    else
      exit 1
    fi
    ;;
esac`}, nil)
	if err != nil {
		return false, errors.New("storage_route_probe_unavailable")
	}
	switch strings.TrimSpace(result.Stdout) {
	case "registered":
		return true, nil
	case "absent":
		return false, nil
	default:
		return false, errors.New("storage_route_probe_unavailable")
	}
}

func readStorageComponents(ctx context.Context, transport setupssh.Client) (map[string]storageComponent, error) {
	result, err := transport.Run(ctx, []string{"sh", "-lc", `set -eu
for name in storage-primary storage-node storage-view; do
  if "$HOME/moox/storage/status.sh" "$name" >/dev/null 2>&1; then printf '%s ready\n' "$name"; else printf '%s unhealthy\n' "$name"; fi
done`}, nil)
	if err != nil {
		return nil, errors.New("storage_component_unavailable")
	}
	components := make(map[string]storageComponent, 3)
	for _, line := range strings.Split(result.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && (fields[1] == "ready" || fields[1] == "unhealthy") {
			components[fields[0]] = storageComponent{Status: fields[1]}
		}
	}
	for _, name := range []string{"storage-primary", "storage-node", "storage-view"} {
		if components[name].Status != "ready" {
			return nil, errors.New("storage_component_unavailable")
		}
	}
	return components, nil
}

func readStorageBinaryHashes(ctx context.Context, transport setupssh.Client) (map[string]string, error) {
	result, err := transport.Run(ctx, []string{"sh", "-lc", `set -eu
for name in moox-storage-primary moox-storage-node moox-storage-view; do
  hash=$(sha256sum "$HOME/moox/storage/bin/$name" | awk '{print $1}')
  printf '%s %s\n' "$name" "$hash"
done`}, nil)
	if err != nil {
		return nil, errors.New("storage_binary_unavailable")
	}
	hashes := make(map[string]string, 3)
	for _, line := range strings.Split(result.Stdout, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[1]) != 64 {
			continue
		}
		if _, err := hex.DecodeString(fields[1]); err != nil {
			continue
		}
		hashes[fields[0]] = strings.ToLower(fields[1])
	}
	for _, name := range []string{"moox-storage-primary", "moox-storage-node", "moox-storage-view"} {
		if len(hashes[name]) != 64 {
			return nil, errors.New("storage_binary_unavailable")
		}
	}
	return hashes, nil
}

func readStorageSchemaVersion(ctx context.Context, transport setupssh.Client) (int, error) {
	result, err := transport.Run(ctx, []string{"sh", "-lc", `set -eu
db="$HOME/moox/data/storage/metadata/storage_metadata.db"
version=$(sqlite3 -readonly "$db" "SELECT c_value FROM t_schema_meta WHERE c_key = 'schema_version';")
printf '%s' "$version"`}, nil)
	if err != nil {
		return 0, errors.New("storage_schema_unavailable")
	}
	version, err := strconv.Atoi(strings.TrimSpace(result.Stdout))
	if err != nil || version != 5 {
		return 0, errors.New("storage_schema_incompatible")
	}
	return version, nil
}

func storageTargetAddress(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "ip" || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", errors.New("invalid service target")
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || strings.TrimSpace(host) == "" {
		return "", errors.New("invalid service target")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return "", errors.New("invalid service target")
	}
	return net.JoinHostPort(host, strconv.Itoa(portNumber)), nil
}

func listAllStorageDataNodes(ctx context.Context, api storageMetadataAPI, auth *storagepb.AuthInfo) ([]*storagepb.DataNodeListItem, error) {
	if api == nil {
		return nil, errors.New("storage_metadata_unavailable")
	}
	var all []*storagepb.DataNodeListItem
	for page := uint32(1); ; page++ {
		response, err := api.ListDataNodes(ctx, &storagepb.ListDataNodesReq{AuthInfo: auth, Page: &storagepb.Page{Page: page, Size: 500}})
		if err != nil || response == nil || response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			return nil, errors.New("storage_metadata_unavailable")
		}
		all = append(all, response.GetItems()...)
		if response.GetPageResult() == nil || !response.GetPageResult().GetHasMore() {
			return all, nil
		}
		if page >= 10000 {
			return nil, errors.New("storage_metadata_unavailable")
		}
	}
}

func runStorageLifecycle(ctx context.Context, session *remoteStorageSession, namespace string) (result storageE2EResult, returnErr error) {
	if session == nil || session.metadata == nil || session.primary == nil {
		return storageE2EResult{}, errors.New("storage_e2e_unavailable")
	}
	if err := validateStorageNamespace(namespace); err != nil {
		return storageE2EResult{}, err
	}
	result = storageE2EResult{Status: "running", Namespace: namespace, Assertions: []string{}, Cleanup: "pending"}
	spaceID := namespace + "_space"
	sourceID := namespace + "_source"
	datasetID := namespace + "_dataset"
	var space *storagepb.Space
	var source *storagepb.DataSource
	var dataset *storagepb.Dataset
	var rowKey *storagepb.RowKey
	var cleanupErr error
	result.Skipped = []string{"second_data_node_runtime", "empty_disabled_node_delete"}
	defer func() {
		cleanupErr = cleanupStorageLifecycle(ctx, session, space, source, []*storagepb.Dataset{dataset})
		if cleanupErr != nil {
			result.Cleanup = "failed"
			if returnErr == nil {
				returnErr = errors.New("storage_e2e_cleanup_failed")
			}
		} else {
			result.Cleanup = "completed"
		}
		if returnErr == nil {
			result.Status = "passed"
		}
	}()

	items, err := listAllStorageDataNodes(ctx, session.metadata, session.auth)
	if err != nil {
		return result, errors.New("storage_e2e_metadata_unavailable")
	}
	deployedNode, err := selectDeploymentDataNode(items)
	if err != nil {
		return result, err
	}
	space = &storagepb.Space{SpaceId: spaceID, Name: "E2E 临时空间", Owner: "storage-e2e", Status: "active"}
	spaceResponse, err := session.metadata.CreateSpace(ctx, &storagepb.CreateSpaceReq{AuthInfo: session.auth, Space: space})
	if err != nil || spaceResponse == nil || spaceResponse.GetRetInfo() == nil || spaceResponse.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return result, errors.New("storage_e2e_space_failed")
	}
	result.Assertions = append(result.Assertions, "space_created")
	source = &storagepb.DataSource{SpaceId: spaceID, DataSourceId: sourceID, Name: "E2E 临时来源", Kind: "e2e", Status: "active"}
	sourceResponse, err := session.metadata.CreateDataSource(ctx, &storagepb.CreateDataSourceReq{AuthInfo: session.auth, DataSource: source})
	if err != nil || sourceResponse == nil || sourceResponse.GetRetInfo() == nil || sourceResponse.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return result, errors.New("storage_e2e_data_source_failed")
	}
	result.Assertions = append(result.Assertions, "data_source_created")
	dataset = &storagepb.Dataset{SpaceId: spaceID, DatasetId: datasetID, DataSourceId: sourceID, Name: "E2E 临时集", DataKind: storagepb.DataKind_DATA_KIND_RECORD, KeepDuration: "0", Status: "disabled", DataNodeId: deployedNode.GetNodeId()}
	datasetResponse, err := session.metadata.CreateDataset(ctx, &storagepb.CreateDatasetReq{AuthInfo: session.auth, Dataset: dataset})
	if err != nil || datasetResponse == nil || datasetResponse.GetRetInfo() == nil || datasetResponse.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || datasetResponse.GetDataset() == nil {
		return result, errors.New("storage_e2e_dataset_failed")
	}
	dataset = datasetResponse.GetDataset()
	result.Assertions = append(result.Assertions, "dataset_created_disabled")
	columnResponse, err := session.metadata.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{AuthInfo: session.auth, Column: &storagepb.DatasetColumn{
		SpaceId: spaceID, DatasetId: datasetID, ColumnName: "value", OriginId: "value", ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active", Attributes: map[string]string{"display_name": "数值"},
	}})
	if err != nil || columnResponse == nil || columnResponse.GetRetInfo() == nil || columnResponse.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return result, errors.New("storage_e2e_dataset_column_failed")
	}
	result.Assertions = append(result.Assertions, "dataset_column_created")
	rowKey = &storagepb.RowKey{SpaceId: spaceID, DatasetId: datasetID, Kind: &storagepb.RowKey_Record{Record: &storagepb.RecordRowKey{RecordId: "row-1", Version: "1"}}}
	row := &storagepb.RowFieldUpsert{Key: rowKey, Fields: []*storagepb.FieldValue{{FieldId: "value", Value: &storagepb.TypedValue{Value: &storagepb.TypedValue_StringValue{StringValue: "row-value"}}}}, Operation: storagepb.RowFieldOperation_ROW_FIELD_OPERATION_UPSERT}
	disabledWrite, err := session.primary.UpsertFields(ctx, &storagepb.PrimaryUpsertFieldsReq{AuthInfo: session.primaryAuth, Rows: []*storagepb.RowFieldUpsert{row}})
	if err != nil || disabledWrite == nil || disabledWrite.GetRetInfo() == nil || disabledWrite.GetRetInfo().GetCode() == storagepb.ErrorCode_SUCCESS {
		return result, errors.New("storage_e2e_disabled_write_accepted")
	}
	result.Assertions = append(result.Assertions, "disabled_write_rejected")
	checkResponse, err := session.metadata.CheckDatasetActivation(ctx, &storagepb.CheckDatasetActivationReq{AuthInfo: session.auth, SpaceId: spaceID, DatasetId: datasetID})
	if err != nil || checkResponse == nil || checkResponse.GetRetInfo() == nil || checkResponse.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || !checkResponse.GetReady() {
		return result, errors.New("storage_e2e_activation_check_failed")
	}
	result.Assertions = append(result.Assertions, "activation_checks_passed")
	staleRevision := checkResponse.GetDatasetRevision()
	if staleRevision > 0 {
		staleRevision--
	}
	staleActivation, err := session.metadata.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{AuthInfo: session.auth, SpaceId: spaceID, DatasetId: datasetID, ExpectedRevision: staleRevision})
	if err != nil || staleActivation == nil || staleActivation.GetRetInfo() == nil || staleActivation.GetRetInfo().GetCode() == storagepb.ErrorCode_SUCCESS {
		return result, errors.New("storage_e2e_stale_revision_accepted")
	}
	result.Assertions = append(result.Assertions, "stale_revision_rejected")
	activated, err := session.metadata.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{AuthInfo: session.auth, SpaceId: spaceID, DatasetId: datasetID, ExpectedRevision: checkResponse.GetDatasetRevision()})
	if err != nil || activated == nil || activated.GetRetInfo() == nil || activated.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || activated.GetDataset() == nil || activated.GetDataset().GetStatus() != "active" || !activated.GetDataset().GetBindingLocked() {
		return result, errors.New("storage_e2e_activation_failed")
	}
	dataset = activated.GetDataset()
	result.Assertions = append(result.Assertions, "dataset_activated_locked")
	writeResponse, err := session.primary.UpsertFields(ctx, &storagepb.PrimaryUpsertFieldsReq{AuthInfo: session.primaryAuth, Rows: []*storagepb.RowFieldUpsert{row}})
	if err != nil || writeResponse == nil || writeResponse.GetRetInfo() == nil || writeResponse.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || len(writeResponse.GetKeys()) != 1 {
		return result, errors.New("storage_e2e_write_failed")
	}
	result.Assertions = append(result.Assertions, "row_written")
	readResponse, err := session.primary.ReadFields(ctx, &storagepb.PrimaryReadFieldsReq{AuthInfo: session.primaryAuth, Keys: []*storagepb.RowKey{rowKey}, FieldIds: []string{"value"}})
	if err != nil || readResponse == nil || readResponse.GetRetInfo() == nil || readResponse.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || len(readResponse.GetRows()) != 1 || len(readResponse.GetRows()[0].GetFields()) != 1 || readResponse.GetRows()[0].GetFields()[0].GetValue().GetStringValue() != "row-value" {
		return result, errors.New("storage_e2e_read_failed")
	}
	result.Assertions = append(result.Assertions, "row_read_back")
	rebindResponse, err := session.metadata.RebindDatasetDataNode(ctx, &storagepb.RebindDatasetDataNodeReq{AuthInfo: session.auth, SpaceId: spaceID, DatasetId: datasetID, DataNodeId: namespace + "_unregistered_node", ExpectedRevision: dataset.GetRevision()})
	if err != nil || rebindResponse == nil || rebindResponse.GetRetInfo() == nil || rebindResponse.GetRetInfo().GetCode() == storagepb.ErrorCode_SUCCESS {
		return result, errors.New("storage_e2e_locked_rebind_accepted")
	}
	result.Assertions = append(result.Assertions, "locked_rebind_rejected")
	activeDelete, err := session.metadata.DeleteDataNode(ctx, &storagepb.DeleteDataNodeReq{AuthInfo: session.auth, NodeId: deployedNode.GetNodeId()})
	if err != nil || activeDelete == nil || activeDelete.GetRetInfo() == nil || activeDelete.GetRetInfo().GetCode() == storagepb.ErrorCode_SUCCESS {
		return result, errors.New("storage_e2e_active_node_delete_accepted")
	}
	result.Assertions = append(result.Assertions, "active_node_delete_rejected")
	return result, nil
}

func cleanupStorageLifecycle(ctx context.Context, session *remoteStorageSession, space *storagepb.Space, source *storagepb.DataSource, datasets []*storagepb.Dataset) error {
	if session == nil {
		return errors.New("storage_e2e_cleanup_failed")
	}
	var first error
	for _, dataset := range datasets {
		if dataset == nil {
			continue
		}
		if err := deleteStorageDataset(ctx, session, dataset); err != nil && first == nil {
			first = err
		}
	}
	if source != nil {
		response, err := session.metadata.DeleteDataSource(ctx, &storagepb.DeleteDataSourceReq{AuthInfo: session.auth, SpaceId: source.GetSpaceId(), DataSourceId: source.GetDataSourceId()})
		if err != nil || response == nil || response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			if first == nil {
				first = errors.New("data source cleanup failed")
			}
		}
	}
	if space != nil {
		response, err := session.metadata.DeleteSpace(ctx, &storagepb.DeleteSpaceReq{AuthInfo: session.auth, SpaceId: space.GetSpaceId()})
		if err != nil || response == nil || response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			if first == nil {
				first = errors.New("space cleanup failed")
			}
		}
	}
	return first
}

func deleteStorageDataset(ctx context.Context, session *remoteStorageSession, dataset *storagepb.Dataset) error {
	response, err := session.metadata.DeleteDataset(ctx, &storagepb.DeleteDatasetReq{AuthInfo: session.auth, SpaceId: dataset.GetSpaceId(), DatasetId: dataset.GetDatasetId()})
	if err != nil || response == nil || response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return errors.New("dataset cleanup failed")
	}
	return nil
}

func validateStorageNamespace(namespace string) error {
	namespace = strings.TrimSpace(namespace)
	if namespace == "" || len(namespace) > 18 {
		return errors.New("storage_e2e_namespace_invalid")
	}
	for index, r := range namespace {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' {
			if index == 0 && (r == '_' || r == '-') {
				return errors.New("storage_e2e_namespace_invalid")
			}
			continue
		}
		return errors.New("storage_e2e_namespace_invalid")
	}
	return nil
}

func newStorageBrowserNamespace() string {
	suffix := strconv.FormatInt(time.Now().UTC().UnixNano(), 36)
	if len(suffix) > 8 {
		suffix = suffix[len(suffix)-8:]
	}
	return "br" + suffix
}

func mustMarshalStorageBrowserFixture(fixture storageBrowserFixture) string {
	raw, err := json.Marshal(fixture)
	if err != nil {
		panic("storage browser fixture is not marshalable")
	}
	return string(raw)
}

func createStorageBrowserFixture(ctx context.Context, session *remoteStorageSession, adminSpaces storageAdminSpaceAPI) (fixture storageBrowserFixture, cleanup func() error, returnErr error) {
	if session == nil || session.metadata == nil || adminSpaces == nil {
		return storageBrowserFixture{}, nil, errors.New("browser_e2e_fixture_unavailable")
	}
	namespace := newStorageBrowserNamespace()
	spaceID := namespace + "_space"
	sourceID := namespace + "_source"
	datasetID := namespace + "_dataset"
	fixture = storageBrowserFixture{Namespace: namespace, SpaceID: spaceID, SourceID: sourceID, DatasetID: datasetID, DatasetName: "浏览器验证集"}
	var adminSpace *adminpb.Space
	var storageSpace *storagepb.Space
	var source *storagepb.DataSource
	var dataset *storagepb.Dataset
	cleanup = func() error {
		var first error
		if err := cleanupStorageLifecycle(ctx, session, storageSpace, source, []*storagepb.Dataset{dataset}); err != nil {
			first = err
		}
		if adminSpace != nil {
			response, err := adminSpaces.UpdateSpace(ctx, &adminpb.UpdateSpaceReq{Space: &adminpb.Space{
				SpaceId: adminSpace.GetSpaceId(), Name: adminSpace.GetName(), Owner: adminSpace.GetOwner(), Status: "disabled",
			}})
			if err != nil || response == nil || response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS {
				if first == nil {
					first = errors.New("browser_e2e_admin_space_cleanup_failed")
				}
			}
		}
		return first
	}
	defer func() {
		if returnErr != nil && cleanup != nil {
			if cleanupErr := cleanup(); cleanupErr != nil {
				returnErr = errors.New("browser_e2e_fixture_cleanup_failed")
			}
		}
	}()

	adminResponse, err := adminSpaces.CreateSpace(ctx, &adminpb.CreateSpaceReq{Space: &adminpb.Space{
		SpaceId: spaceID, Name: "浏览器隔离空间", Owner: "storage-e2e", Status: "active",
	}})
	if err != nil || adminResponse == nil || adminResponse.GetRetInfo() == nil || adminResponse.GetRetInfo().GetCode() != adminpb.ErrorCode_SUCCESS || adminResponse.GetSpace() == nil {
		return fixture, cleanup, errors.New("browser_e2e_admin_space_create_failed")
	}
	adminSpace = adminResponse.GetSpace()
	storageSpace = &storagepb.Space{SpaceId: spaceID, Name: "浏览器隔离空间", Owner: "storage-e2e", Status: "active"}
	spaceResponse, err := session.metadata.CreateSpace(ctx, &storagepb.CreateSpaceReq{AuthInfo: session.auth, Space: storageSpace})
	if err != nil || spaceResponse == nil || spaceResponse.GetRetInfo() == nil || spaceResponse.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || spaceResponse.GetSpace() == nil {
		return fixture, cleanup, errors.New("browser_e2e_storage_space_create_failed")
	}
	storageSpace = spaceResponse.GetSpace()
	source = &storagepb.DataSource{SpaceId: spaceID, DataSourceId: sourceID, Name: "浏览器隔离来源", Kind: "e2e", Status: "active"}
	sourceResponse, err := session.metadata.CreateDataSource(ctx, &storagepb.CreateDataSourceReq{AuthInfo: session.auth, DataSource: source})
	if err != nil || sourceResponse == nil || sourceResponse.GetRetInfo() == nil || sourceResponse.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || sourceResponse.GetDataSource() == nil {
		return fixture, cleanup, errors.New("browser_e2e_data_source_create_failed")
	}
	source = sourceResponse.GetDataSource()
	dataset = &storagepb.Dataset{SpaceId: spaceID, DatasetId: datasetID, DataSourceId: sourceID, Name: fixture.DatasetName, DataKind: storagepb.DataKind_DATA_KIND_RECORD, KeepDuration: "0", Status: "disabled", DataNodeId: storageDeploymentNodeID}
	items, err := listAllStorageDataNodes(ctx, session.metadata, session.auth)
	if err != nil {
		return fixture, cleanup, errors.New("browser_e2e_data_node_list_failed")
	}
	node, err := selectDeploymentDataNode(items)
	if err != nil {
		return fixture, cleanup, err
	}
	dataset.DataNodeId = node.GetNodeId()
	datasetResponse, err := session.metadata.CreateDataset(ctx, &storagepb.CreateDatasetReq{AuthInfo: session.auth, Dataset: dataset})
	if err != nil || datasetResponse == nil || datasetResponse.GetRetInfo() == nil || datasetResponse.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || datasetResponse.GetDataset() == nil {
		return fixture, cleanup, errors.New("browser_e2e_dataset_create_failed")
	}
	dataset = datasetResponse.GetDataset()
	columnResponse, err := session.metadata.UpsertDatasetColumn(ctx, &storagepb.UpsertDatasetColumnReq{AuthInfo: session.auth, Column: &storagepb.DatasetColumn{
		SpaceId: spaceID, DatasetId: datasetID, ColumnName: "value", OriginId: "value", ValueType: storagepb.FieldValueType_FIELD_VALUE_TYPE_STRING, Status: "active",
	}})
	if err != nil || columnResponse == nil || columnResponse.GetRetInfo() == nil || columnResponse.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
		return fixture, cleanup, errors.New("browser_e2e_dataset_column_create_failed")
	}
	return fixture, cleanup, nil
}

func defaultSetupBrowserE2EStorage(ctx context.Context, snapshot *setupconfig.Snapshot, name, repoRoot string) (storageBrowserResult, error) {
	if snapshot == nil || strings.TrimSpace(repoRoot) == "" {
		return storageBrowserResult{}, errors.New("browser_e2e_invalid")
	}
	host, err := resolveStorageBrowserHost(snapshot.Manifest, name)
	if err != nil {
		return storageBrowserResult{}, err
	}
	_, storageTransport, storageSession, _, err := openRemoteStorage(ctx, snapshot, name)
	if err != nil {
		return storageBrowserResult{}, err
	}
	defer storageTransport.Close()
	defer storageSession.Close()
	controlTransport, err := dialSetupHost(ctx, host)
	if err != nil {
		return storageBrowserResult{}, errors.New("browser_e2e_control_unavailable")
	}
	defer controlTransport.Close()
	if _, err := controlTransport.Run(ctx, []string{"sh", "-lc", `set -eu
"$HOME/moox/prod/status.sh" admin >/dev/null
"$HOME/moox/prod/status.sh" gateway >/dev/null
"$HOME/moox/prod/status.sh" web-host >/dev/null
curl -kfsS https://127.0.0.1:9527/ >/dev/null`}, nil); err != nil {
		return storageBrowserResult{}, errors.New("browser_e2e_control_unavailable")
	}
	adminListener, err := controlTransport.ForwardLocal(ctx, adminSpaceRemoteAddress)
	if err != nil {
		return storageBrowserResult{}, errors.New("browser_e2e_control_unavailable")
	}
	defer adminListener.Close()
	adminOptions := []client.Option{client.WithTarget("ip://" + adminListener.Addr().String()), client.WithNetwork("tcp"), client.WithProtocol("http")}
	adminSpaces := &storageAdminSpaceProxy{proxy: adminpb.NewSpaceMgrClientProxy(adminOptions...), options: adminOptions}
	fixture, cleanup, err := createStorageBrowserFixture(ctx, storageSession, adminSpaces)
	if err != nil {
		return storageBrowserResult{}, err
	}
	cleaned := false
	defer func() {
		if !cleaned {
			if cleanupErr := cleanup(); cleanupErr != nil {
				// Cleanup errors are returned by the caller below; this defer is only a
				// last-resort guard for early command failures.
				_ = cleanupErr
			}
		}
	}()
	forwardContext, cancel := context.WithCancel(ctx)
	defer cancel()
	listener, err := controlTransport.ForwardLocal(forwardContext, storageBrowserRemoteAddress)
	if err != nil {
		return storageBrowserResult{}, errors.New("browser_e2e_unreachable")
	}
	defer listener.Close()
	root, err := filepath.Abs(repoRoot)
	if err != nil {
		return storageBrowserResult{}, errors.New("browser_e2e_invalid")
	}
	command := exec.CommandContext(ctx, "pnpm", "--dir", "web", "exec", "playwright", "test", storageE2ESpec, "--project=chromium")
	command.Dir = root
	baseURL := "https://" + listener.Addr().String()
	command.Env = append(os.Environ(),
		"MOOX_REMOTE_PLAYWRIGHT=1",
		"MOOX_REMOTE_BASE_URL="+baseURL,
		"MOOX_REMOTE_STORAGE_FIXTURE="+mustMarshalStorageBrowserFixture(fixture),
		"MOOX_REMOTE_TRACE=off",
		"MOOX_REMOTE_VIDEO=off",
	)
	stdin, err := command.StdinPipe()
	if err != nil {
		return storageBrowserResult{}, errors.New("browser_e2e_invalid")
	}
	if err := command.Start(); err != nil {
		_ = stdin.Close()
		return storageBrowserResult{}, errors.New("browser_e2e_start_failed")
	}
	credentials := map[string]string{"base_url": baseURL, "username": snapshot.Manifest.Admin.Username, "password": snapshot.Manifest.Admin.Password}
	encodeErr := json.NewEncoder(stdin).Encode(credentials)
	_ = stdin.Close()
	waitErr := command.Wait()
	cleanupErr := cleanup()
	cleaned = true
	if encodeErr != nil || waitErr != nil {
		return storageBrowserResult{}, errors.New("browser_e2e_failed")
	}
	if cleanupErr != nil {
		return storageBrowserResult{}, errors.New("browser_e2e_cleanup_failed")
	}
	return storageBrowserResult{Status: "passed", Desktop: "passed", Mobile: "passed"}, nil
}

func resolveStorageBrowserHost(manifest setupconfig.Manifest, requestedStorageHost string) (setupconfig.Host, error) {
	if _, err := findSetupHost(manifest, requestedStorageHost); err != nil {
		return setupconfig.Host{}, err
	}
	control := manifest.ControlHost
	if strings.TrimSpace(control.Name) == "" || strings.TrimSpace(control.Address) == "" {
		return setupconfig.Host{}, errors.New("browser_e2e_control_unavailable")
	}
	return control, nil
}
