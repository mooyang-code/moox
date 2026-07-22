package command

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

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
	storageBrowserRemoteAddress  = "127.0.0.1:9527"
	storageAuthFile              = "$HOME/moox/storage/secrets/storage-node-auth.env"
	storagePrimaryAuthFile       = "$HOME/moox/storage/secrets/gateway-storage-primary.key"
	storageE2ESpec               = "tests/storage-datanode-management.remote.e2e.spec.ts"
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
	Cleanup    string   `json:"cleanup"`
}

type storageBrowserResult struct {
	Status  string `json:"status"`
	Desktop string `json:"desktop"`
	Mobile  string `json:"mobile"`
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
	RebindDatasetDataNode(context.Context, *storagepb.RebindDatasetDataNodeReq) (*storagepb.RebindDatasetDataNodeRsp, error)
	DeleteDataNode(context.Context, *storagepb.DeleteDataNodeReq) (*storagepb.DeleteDataNodeRsp, error)
	ListDataNodes(context.Context, *storagepb.ListDataNodesReq) (*storagepb.ListDataNodesRsp, error)
	CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error)
	ActivateDataset(context.Context, *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error)
}

type storagePrimaryAPI interface {
	WriteFields(context.Context, *storagepb.PrimaryWriteFieldsReq) (*storagepb.PrimaryWriteFieldsRsp, error)
	ReadFields(context.Context, *storagepb.PrimaryReadFieldsReq) (*storagepb.PrimaryReadFieldsRsp, error)
	DeleteFields(context.Context, *storagepb.PrimaryDeleteFieldsReq) (*storagepb.PrimaryDeleteFieldsRsp, error)
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

func (c *storagePrimaryProxy) WriteFields(ctx context.Context, req *storagepb.PrimaryWriteFieldsReq) (*storagepb.PrimaryWriteFieldsRsp, error) {
	return c.proxy.WriteFields(ctx, req, c.options...)
}

func (c *storagePrimaryProxy) ReadFields(ctx context.Context, req *storagepb.PrimaryReadFieldsReq) (*storagepb.PrimaryReadFieldsRsp, error) {
	return c.proxy.ReadFields(ctx, req, c.options...)
}

func (c *storagePrimaryProxy) DeleteFields(ctx context.Context, req *storagepb.PrimaryDeleteFieldsReq) (*storagepb.PrimaryDeleteFieldsRsp, error) {
	return c.proxy.DeleteFields(ctx, req, c.options...)
}

func (c *storageRuntimeProxy) GetNodeState(ctx context.Context, req *storagepb.GetNodeStateReq) (*storagepb.GetNodeStateRsp, error) {
	return c.proxy.GetNodeState(ctx, req, c.options...)
}

type remoteStorageSession struct {
	transport       setupssh.Client
	metadata        storageMetadataAPI
	primary         storagePrimaryAPI
	auth            *storagepb.AuthInfo
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
	host, transport, session, root, err := openRemoteStorage(ctx, snapshot, name)
	if err != nil {
		return storageVerifyResult{}, err
	}
	defer transport.Close()
	defer session.Close()
	return verifyRemoteStorage(ctx, transport, session, host.Name, root)
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

func verifyRemoteStorage(ctx context.Context, transport setupssh.Client, session *remoteStorageSession, hostName, root string) (storageVerifyResult, error) {
	components, err := readStorageComponents(ctx, transport)
	if err != nil {
		return storageVerifyResult{}, err
	}
	hashes, err := readStorageBinaryHashes(ctx, transport)
	if err != nil {
		return storageVerifyResult{}, err
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
		if selected == nil && item.GetNode() != nil && item.GetNode().GetStatus() == "active" {
			selected = item.GetNode()
		}
	}
	if selected == nil {
		return storageVerifyResult{}, errors.New("storage_verification_failed")
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
	commit, err := localGitCommit(ctx, root)
	if err != nil {
		return storageVerifyResult{}, errors.New("storage_verification_commit_unavailable")
	}
	_ = hostName
	return storageVerifyResult{
		Status:             "passed",
		Commit:             commit,
		Components:         components,
		BinaryHashes:       hashes,
		SchemaVersion:      schemaVersion,
		DataNode:           storageDataNodeIdentity{NodeID: state.GetNodeId(), Status: state.GetStatus()},
		NodeCount:          len(items),
		DatasetCount:       datasetCount,
		RouteRPCRegistered: false,
	}, nil
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

func localGitCommit(ctx context.Context, root string) (string, error) {
	output, err := exec.CommandContext(ctx, "git", "-C", root, "rev-parse", "HEAD").Output()
	if err != nil {
		return "", err
	}
	commit := strings.TrimSpace(string(output))
	if len(commit) != 40 {
		return "", errors.New("invalid commit")
	}
	for _, r := range commit {
		if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
			return "", errors.New("invalid commit")
		}
	}
	return strings.ToLower(commit), nil
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
	if session == nil || session.metadata == nil {
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
	var cleanupErr error
	defer func() {
		cleanupErr = cleanupStorageLifecycle(ctx, session, space, source, dataset)
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
	var nodeID string
	for _, item := range items {
		if item != nil && item.GetNode() != nil && item.GetNode().GetStatus() == "active" {
			nodeID = item.GetNode().GetNodeId()
			break
		}
	}
	if nodeID == "" {
		return result, errors.New("storage_e2e_node_unavailable")
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
	dataset = &storagepb.Dataset{SpaceId: spaceID, DatasetId: datasetID, DataSourceId: sourceID, Name: "E2E 临时集", DataKind: storagepb.DataKind_DATA_KIND_TIME_SERIES, Freqs: []string{"1m"}, KeepDuration: "1h", Status: "disabled", DataNodeId: nodeID}
	datasetResponse, err := session.metadata.CreateDataset(ctx, &storagepb.CreateDatasetReq{AuthInfo: session.auth, Dataset: dataset})
	if err != nil || datasetResponse == nil || datasetResponse.GetRetInfo() == nil || datasetResponse.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || datasetResponse.GetDataset() == nil {
		return result, errors.New("storage_e2e_dataset_failed")
	}
	dataset = datasetResponse.GetDataset()
	result.Assertions = append(result.Assertions, "dataset_created_disabled")
	checkResponse, err := session.metadata.CheckDatasetActivation(ctx, &storagepb.CheckDatasetActivationReq{AuthInfo: session.auth, SpaceId: spaceID, DatasetId: datasetID})
	if err != nil || checkResponse == nil || checkResponse.GetRetInfo() == nil || checkResponse.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || !checkResponse.GetReady() {
		return result, errors.New("storage_e2e_activation_check_failed")
	}
	result.Assertions = append(result.Assertions, "activation_checks_passed")
	activated, err := session.metadata.ActivateDataset(ctx, &storagepb.ActivateDatasetReq{AuthInfo: session.auth, SpaceId: spaceID, DatasetId: datasetID, ExpectedRevision: checkResponse.GetDatasetRevision()})
	if err != nil || activated == nil || activated.GetRetInfo() == nil || activated.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS || activated.GetDataset() == nil || activated.GetDataset().GetStatus() != "active" || !activated.GetDataset().GetBindingLocked() {
		return result, errors.New("storage_e2e_activation_failed")
	}
	dataset = activated.GetDataset()
	result.Assertions = append(result.Assertions, "dataset_activated_locked")
	return result, nil
}

func cleanupStorageLifecycle(ctx context.Context, session *remoteStorageSession, space *storagepb.Space, source *storagepb.DataSource, dataset *storagepb.Dataset) error {
	if session == nil {
		return errors.New("storage_e2e_cleanup_failed")
	}
	var first error
	if dataset != nil {
		copy := *dataset
		copy.Status = "disabled"
		response, err := session.metadata.UpdateDataset(ctx, &storagepb.UpdateDatasetReq{AuthInfo: session.auth, Dataset: &copy})
		if err != nil || response == nil || response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			first = errors.New("dataset cleanup failed")
		}
	}
	if source != nil {
		copy := *source
		copy.Status = "disabled"
		response, err := session.metadata.UpdateDataSource(ctx, &storagepb.UpdateDataSourceReq{AuthInfo: session.auth, DataSource: &copy})
		if err != nil || response == nil || response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			if first == nil {
				first = errors.New("data source cleanup failed")
			}
		}
	}
	if space != nil {
		copy := *space
		copy.Status = "disabled"
		response, err := session.metadata.UpdateSpace(ctx, &storagepb.UpdateSpaceReq{AuthInfo: session.auth, Space: &copy})
		if err != nil || response == nil || response.GetRetInfo() == nil || response.GetRetInfo().GetCode() != storagepb.ErrorCode_SUCCESS {
			if first == nil {
				first = errors.New("space cleanup failed")
			}
		}
	}
	return first
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

func defaultSetupBrowserE2EStorage(ctx context.Context, snapshot *setupconfig.Snapshot, name, repoRoot string) (storageBrowserResult, error) {
	if snapshot == nil || strings.TrimSpace(repoRoot) == "" {
		return storageBrowserResult{}, errors.New("browser_e2e_invalid")
	}
	host, err := findSetupHost(snapshot.Manifest, name)
	if err != nil {
		return storageBrowserResult{}, err
	}
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return storageBrowserResult{}, err
	}
	defer transport.Close()
	forwardContext, cancel := context.WithCancel(ctx)
	defer cancel()
	listener, err := transport.ForwardLocal(forwardContext, storageBrowserRemoteAddress)
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
	command.Env = append(os.Environ(),
		"MOOX_REMOTE_PLAYWRIGHT=1",
		"MOOX_REMOTE_BASE_URL=http://"+listener.Addr().String(),
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
	credentials := map[string]string{"base_url": "http://" + listener.Addr().String(), "username": snapshot.Manifest.Admin.Username, "password": snapshot.Manifest.Admin.Password}
	encodeErr := json.NewEncoder(stdin).Encode(credentials)
	_ = stdin.Close()
	waitErr := command.Wait()
	if encodeErr != nil || waitErr != nil {
		return storageBrowserResult{}, errors.New("browser_e2e_failed")
	}
	return storageBrowserResult{Status: "passed", Desktop: "passed", Mobile: "passed"}, nil
}
