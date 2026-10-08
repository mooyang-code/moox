// Package snapshot 校验并持有主机网关当前应用的快照：本机路由、全局服务目录和调用方校验密钥。
// 新快照先整体校验，任何一项不通过就整体拒绝，继续使用旧快照。
package snapshot

import (
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/gatewayclient"
	"github.com/mooyang-code/moox/packages/gatewayroute"
	"github.com/mooyang-code/moox/packages/gatewayroute/proto/directorypb"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"google.golang.org/protobuf/proto"
)

// ErrInvalid 表示快照没有通过校验。
var ErrInvalid = errors.New("主机网关快照无效")

// Applied 是校验通过后的快照。
type Applied struct {
	HostID    string
	Hash      string
	Disabled  bool
	Proto     *adminpb.HostSnapshot
	Table     *gatewayroute.Table
	Registry  *gatewayauth.CredentialRegistry
	Directory *directorypb.DirectorySnapshot
	Routes    int
}

// Validate 校验一份快照属于本机、路由合法、目录版本与内容一致、哈希覆盖路由、目录和密钥。
func Validate(hostID string, snapshot *adminpb.HostSnapshot) (*Applied, error) {
	if snapshot == nil {
		return nil, fmt.Errorf("%w: 快照为空", ErrInvalid)
	}
	if snapshot.GetHostId() != hostID {
		return nil, fmt.Errorf("%w: 快照属于主机 %q，本机是 %q", ErrInvalid, snapshot.GetHostId(), hostID)
	}
	routes := make([]gatewayroute.Route, 0, len(snapshot.GetRoutes()))
	for _, route := range snapshot.GetRoutes() {
		routes = append(routes, gatewayroute.Route{
			ServiceID: route.GetComponentId(), Address: route.GetAddress(), ServicePath: route.GetServicePath(),
			TimeoutMS: route.GetTimeoutMs(), MaxBodyBytes: route.GetMaxBodyBytes(),
			AllowedMethods: route.GetMethods(), AllowedCallers: route.GetCallers(),
		})
	}
	normalized, err := gatewayroute.NormalizeAndHashState(hostID, snapshot.GetDisabled(), routes)
	if err != nil {
		return nil, fmt.Errorf("%w: 路由: %v", ErrInvalid, err)
	}
	var table gatewayroute.Table
	if err := table.Replace(normalized); err != nil {
		return nil, fmt.Errorf("%w: 路由: %v", ErrInvalid, err)
	}
	directory := snapshot.GetDirectory()
	if directory == nil || directory.GetVersion() == "" {
		return nil, fmt.Errorf("%w: 缺少服务目录", ErrInvalid)
	}
	version, err := servicecatalog.DirectoryVersion(gatewayclient.DirectoryFromProto(directory))
	if err != nil {
		return nil, fmt.Errorf("%w: 服务目录: %v", ErrInvalid, err)
	}
	if version != directory.GetVersion() {
		return nil, fmt.Errorf("%w: 服务目录版本与内容不一致", ErrInvalid)
	}
	keys := make([]gatewayroute.VerificationKey, 0, len(snapshot.GetKeys()))
	credentials := make([]gatewayauth.Credentials, 0, len(snapshot.GetKeys()))
	for _, key := range snapshot.GetKeys() {
		keys = append(keys, gatewayroute.VerificationKey{KeyID: key.GetKeyId(), Caller: key.GetCaller(), Secret: key.GetSecret()})
		credentials = append(credentials, gatewayauth.Credentials{KeyID: key.GetKeyId(), Caller: key.GetCaller(), Secret: key.GetSecret()})
	}
	var registry *gatewayauth.CredentialRegistry
	if len(credentials) > 0 {
		registry, err = gatewayauth.NewCredentialRegistry(credentials)
		if err != nil {
			return nil, fmt.Errorf("%w: 校验密钥: %v", ErrInvalid, err)
		}
	}
	hash, err := gatewayroute.StateHash(normalized.RouteHash, directory.GetVersion(), keys)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if hash != snapshot.GetHash() {
		return nil, fmt.Errorf("%w: 快照哈希不一致", ErrInvalid)
	}
	return &Applied{
		HostID: hostID, Hash: hash, Disabled: snapshot.GetDisabled(), Proto: proto.Clone(snapshot).(*adminpb.HostSnapshot),
		Table: &table, Registry: registry, Directory: directory, Routes: len(normalized.Routes),
	}, nil
}

// Current 原子地持有当前快照，转发和 Directory 服务并发读取。
type Current struct {
	value     atomic.Pointer[Applied]
	appliedAt atomic.Int64
}

// Store 换上新快照。
func (c *Current) Store(applied *Applied, at time.Time) {
	c.value.Store(applied)
	c.appliedAt.Store(at.Unix())
}

// Load 返回当前快照；还没有快照时返回 nil。
func (c *Current) Load() *Applied { return c.value.Load() }

// Hash 返回当前快照哈希。
func (c *Current) Hash() string {
	if applied := c.value.Load(); applied != nil {
		return applied.Hash
	}
	return ""
}
