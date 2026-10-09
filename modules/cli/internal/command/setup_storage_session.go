package command

import (
	"context"
	"errors"
	"fmt"
	"strings"

	clicgateway "github.com/mooyang-code/moox/modules/cli/internal/gateway"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	setupssh "github.com/mooyang-code/moox/modules/cli/internal/setup/ssh"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/security"
	"trpc.group/trpc-go/trpc-go/client"
)

// storageMetadataAPI 是 setup init 写入元数据、激活数据集用到的 Storage 元数据接口。
type storageMetadataAPI interface {
	CreateSpace(context.Context, *storagepb.CreateSpaceReq) (*storagepb.CreateSpaceRsp, error)
	UpdateSpace(context.Context, *storagepb.UpdateSpaceReq) (*storagepb.UpdateSpaceRsp, error)
	DeleteSpace(context.Context, *storagepb.DeleteSpaceReq) (*storagepb.DeleteSpaceRsp, error)
	ListSpaces(context.Context, *storagepb.ListSpacesReq) (*storagepb.ListSpacesRsp, error)
	CreateDataSource(context.Context, *storagepb.CreateDataSourceReq) (*storagepb.CreateDataSourceRsp, error)
	UpdateDataSource(context.Context, *storagepb.UpdateDataSourceReq) (*storagepb.UpdateDataSourceRsp, error)
	DeleteDataSource(context.Context, *storagepb.DeleteDataSourceReq) (*storagepb.DeleteDataSourceRsp, error)
	CreateDataset(context.Context, *storagepb.CreateDatasetReq) (*storagepb.CreateDatasetRsp, error)
	GetDataset(context.Context, *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error)
	UpdateDataset(context.Context, *storagepb.UpdateDatasetReq) (*storagepb.UpdateDatasetRsp, error)
	DeleteDataset(context.Context, *storagepb.DeleteDatasetReq) (*storagepb.DeleteDatasetRsp, error)
	UpsertDatasetColumn(context.Context, *storagepb.UpsertDatasetColumnReq) (*storagepb.UpsertDatasetColumnRsp, error)
	ListDatasetColumns(context.Context, *storagepb.ListDatasetColumnsReq) (*storagepb.ListDatasetColumnsRsp, error)
	RegisterDataNode(context.Context, *storagepb.RegisterDataNodeReq) (*storagepb.RegisterDataNodeRsp, error)
	UpdateDataNode(context.Context, *storagepb.UpdateDataNodeReq) (*storagepb.UpdateDataNodeRsp, error)
	RebindDatasetDataNode(context.Context, *storagepb.RebindDatasetDataNodeReq) (*storagepb.RebindDatasetDataNodeRsp, error)
	DeleteDataNode(context.Context, *storagepb.DeleteDataNodeReq) (*storagepb.DeleteDataNodeRsp, error)
	ListDataNodes(context.Context, *storagepb.ListDataNodesReq) (*storagepb.ListDataNodesRsp, error)
	CheckDatasetActivation(context.Context, *storagepb.CheckDatasetActivationReq) (*storagepb.CheckDatasetActivationRsp, error)
	ActivateDataset(context.Context, *storagepb.ActivateDatasetReq) (*storagepb.ActivateDatasetRsp, error)
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

func (c *storageMetadataProxy) ListSpaces(ctx context.Context, req *storagepb.ListSpacesReq) (*storagepb.ListSpacesRsp, error) {
	return c.proxy.ListSpaces(ctx, req, c.options...)
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

func (c *storageMetadataProxy) GetDataset(ctx context.Context, req *storagepb.GetDatasetReq) (*storagepb.GetDatasetRsp, error) {
	return c.proxy.GetDataset(ctx, req, c.options...)
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

func (c *storageMetadataProxy) ListDatasetColumns(ctx context.Context, req *storagepb.ListDatasetColumnsReq) (*storagepb.ListDatasetColumnsRsp, error) {
	return c.proxy.ListDatasetColumns(ctx, req, c.options...)
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

// remoteStorageSession 经 control 主机网关调用 Storage 元数据服务；签名密钥取自存储主机的 storage-node-auth.env。
type remoteStorageSession struct {
	gateway  *clicgateway.Client
	metadata storageMetadataAPI
	auth     *storagepb.AuthInfo
}

func (s *remoteStorageSession) Close() {
	if s != nil && s.gateway != nil {
		s.gateway.Close()
	}
}

// openRemoteStorage 连接部署了存储主服务的主机读取元数据签名密钥，并打开经 control 主机网关的 Storage 会话。
// hostID 为空时取部署表中的存储主机；指定时必须是部署了存储主服务的主机。
func openRemoteStorage(ctx context.Context, snapshot *setupconfig.Snapshot, hostID string) (setupssh.Client, *remoteStorageSession, error) {
	if snapshot == nil {
		return nil, nil, errors.New("storage_verification_invalid")
	}
	host, err := storagePrimaryHost(snapshot.Manifest, hostID)
	if err != nil {
		return nil, nil, err
	}
	transport, err := dialSetupHost(ctx, host)
	if err != nil {
		return nil, nil, err
	}
	secret, err := readRemoteStorageSecret(ctx, transport, host.Root)
	if err != nil {
		_ = transport.Close()
		return nil, nil, err
	}
	gateway, err := openControlGateway(snapshot.Manifest)
	if err != nil {
		_ = transport.Close()
		return nil, nil, err
	}
	options := gateway.ClientOptions(gatewayclient.WithTimeout(storageCallTimeout))
	return transport, &remoteStorageSession{
		gateway:  gateway,
		metadata: &storageMetadataProxy{proxy: storagepb.NewMetadataClientProxy(options...)},
		auth:     &storagepb.AuthInfo{AppId: "storage-metadata", AppKey: security.HMACSHA256Hex(secret, []byte("storage-metadata"))},
	}, nil
}

// storagePrimaryHost 返回部署了存储主服务的主机。
func storagePrimaryHost(manifest setupconfig.Manifest, hostID string) (setupconfig.Host, error) {
	hosts := manifest.HostsOf("storage-primary")
	if len(hosts) == 0 {
		return setupconfig.Host{}, errors.New("moox.toml 的部署表中没有存储主服务（storage-primary）")
	}
	hostID = strings.TrimSpace(hostID)
	if hostID == "" {
		hostID = hosts[0]
	}
	for _, id := range hosts {
		if id == hostID {
			host, _ := manifest.Host(id)
			return host, nil
		}
	}
	return setupconfig.Host{}, fmt.Errorf("主机 %s 上没有部署存储主服务（storage-primary）", hostID)
}

func readRemoteStorageSecret(ctx context.Context, transport setupssh.Client, root string) (string, error) {
	result, err := transport.Run(ctx, []string{"sh", "-c", `set -eu
secret_file="$1/secrets/storage-node-auth.env"
value=$(sed -n 's/^MOOX_STORAGE_NODE_AUTH_SECRET=//p' "$secret_file" | head -n 1)
test -n "$value"
case "$value" in *[!A-Za-z0-9._-]*) exit 1 ;; esac
printf '%s' "$value"`, "moox-storage-node-auth", root}, nil)
	if err != nil || strings.TrimSpace(result.Stdout) == "" || strings.ContainsAny(result.Stdout, "\r\n") {
		detail := strings.TrimSpace(result.Stderr)
		if detail != "" {
			return "", fmt.Errorf("storage_verification_auth_unavailable: %s", strings.Join(strings.Fields(detail), " "))
		}
		return "", errors.New("storage_verification_auth_unavailable")
	}
	return strings.TrimSpace(result.Stdout), nil
}
