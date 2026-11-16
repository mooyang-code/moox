// Package snapshot 校验 Admin 下发的快照，并按内嵌的服务目录算出本机的路由、调用名单与中转许可。
// 一次请求读一份不可变的视图。
package snapshot

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync/atomic"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"google.golang.org/protobuf/proto"
)

const (
	MaxBytes = 16 << 20
	// SchemaVersion 是快照的格式版本：主机、部署与公钥，不含路由。
	SchemaVersion = 2
)

var ErrInvalid = errors.New("invalid host gateway snapshot")

type State struct{ current atomic.Pointer[View] }

func (s *State) Load() *View   { return s.current.Load() }
func (s *State) Apply(v *View) { s.current.Store(v) }

type View struct {
	hostID   string
	raw      *pb.HostGatewaySnapshot
	compiled servicecatalog.CompiledHost
	routes   map[string]servicecatalog.Route
	keys     *gatewayauth.Keys
	relays   map[string]servicecatalog.RelayTarget
	topology servicecatalog.Topology
	entries  map[string]bool
}

func (v *View) String() string {
	return fmt.Sprintf("HostSnapshot{host=%s hash=%s routes=%d}", v.HostID(), v.Hash(), v.Count())
}
func (v *View) GoString() string { return v.String() }
func (v *View) HostID() string   { return v.hostID }
func (v *View) Hash() string     { return v.raw.Hash }
func (v *View) Disabled() bool   { return v.compiled.Disabled }
func (v *View) Count() int       { return len(v.routes) }
func (v *View) Proto() *pb.HostGatewaySnapshot {
	return proto.Clone(v.raw).(*pb.HostGatewaySnapshot)
}
func (v *View) Directory() servicecatalog.Directory { return v.compiled.Directory.Clone() }

// Topology 返回快照里的主机与部署，本进程内的网关客户端按它展开目录。
func (v *View) Topology() ([]servicecatalog.Host, []servicecatalog.Deployment) {
	return slices.Clone(v.topology.Hosts), slices.Clone(v.topology.Deployments)
}

// ExternalEntry 报告这台主机是否登记成外部入口：只有这样的主机才接外部身份的请求，
// 别的网关也只认它带来源头转过来的请求。
func (v *View) ExternalEntry(hostID string) bool { return v.entries[hostID] }

// Routes 返回本机的路由（按服务、方法排序），给诊断命令用。
func (v *View) Routes() []servicecatalog.Route { return slices.Clone(v.compiled.Routes) }

// Unknown 是快照里本进程的内嵌目录不认识的组件：它们被忽略，其余照常生效。
func (v *View) Unknown() []string { return slices.Clone(v.compiled.Unknown) }

// Relay 返回本机（control）对一台出站主机的中转许可。
func (v *View) Relay(hostID string) (servicecatalog.RelayTarget, bool) {
	relay, ok := v.relays[hostID]
	return relay, ok
}

// OutboundHost 报告目录里该主机是否只能出站、且由本机中转。
func (v *View) OutboundHost(hostID string) bool {
	host, ok := v.compiled.Directory.Hosts[hostID]
	return ok && host.Relay == v.hostID
}

func (v *View) Resolve(service, method string) (servicecatalog.Route, bool) {
	route, ok := v.routes[service+"/"+method]
	route.Callers = slices.Clone(route.Callers)
	return route, ok
}

// Verify 校验签名与时间窗：带签名主机的用该主机的公钥，并要求调用方确实部署在那台主机上；不带的用非主机身份的公钥。
// 失败原因不回给调用方。
func (v *View) Verify(request gatewayauth.Request, headers http.Header, now time.Time) (gatewayauth.Claims, error) {
	claims, err := v.keys.Verify(request, headers, now)
	if err != nil {
		return gatewayauth.Claims{}, errors.New("host gateway authentication failed")
	}
	return claims, nil
}

// Build 复制输入并整体校验：格式版本、大小、哈希、主机与公钥的格式，任何一项不过就拒绝整份快照。
// 路由、调用名单、中转许可都在本机按内嵌目录算；快照里本进程不认识的组件忽略。
func Build(hostID string, raw *pb.HostGatewaySnapshot) (*View, error) {
	bad := func(reason string) (*View, error) { return nil, fmt.Errorf("%w: %s", ErrInvalid, reason) }
	if !servicecatalog.ValidHostID(hostID) || raw == nil || raw.SchemaVersion != SchemaVersion ||
		len(raw.Hosts) == 0 || len(raw.Hosts) > 1024 || len(raw.Deployments) > 65536 || len(raw.Identities) > 64 || proto.Size(raw) > MaxBytes {
		return bad("host, schema or size")
	}
	if len(raw.ProtoReflect().GetUnknown()) != 0 {
		return bad("unknown schema fields")
	}
	hash, err := pb.SnapshotHash(raw)
	if err != nil || raw.Hash != hash {
		return bad("content hash")
	}
	owned := proto.Clone(raw).(*pb.HostGatewaySnapshot)
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	var hosts []servicecatalog.Host
	public, entries := map[string]ed25519.PublicKey{}, map[string]bool{}
	for _, host := range owned.Hosts {
		if host == nil || len(host.ProtoReflect().GetUnknown()) != 0 || !servicecatalog.ValidHostID(host.HostId) || !servicecatalog.ValidHostAddress(host.Address) ||
			host.PrivateAddress != "" && !servicecatalog.ValidHostAddress(host.PrivateAddress) || len(host.SignPublicKey) != 0 && len(host.SignPublicKey) != ed25519.PublicKeySize {
			return bad("host fields")
		}
		reachability, ok := servicecatalog.ParseReachability(host.Reachability)
		if _, duplicate := public[host.HostId]; duplicate || !ok {
			return bad("duplicate host or reachability")
		}
		// 还没签发过身份包的主机没有公钥，它的请求验不过。
		public[host.HostId] = host.SignPublicKey
		// 停用的主机不算入口：它带来源头转过来的请求不认。
		entries[host.HostId] = host.ExternalEntry && host.Enabled
		hosts = append(hosts, servicecatalog.Host{ID: host.HostId, Address: host.Address, PrivateAddress: host.PrivateAddress, Status: statusName(host.Enabled), Reachability: reachability, ExternalEntry: host.ExternalEntry})
	}
	if _, ok := public[hostID]; !ok {
		return bad("local host is not in the snapshot")
	}
	var deployments []servicecatalog.Deployment
	placed := map[string]bool{}
	for _, deployment := range owned.Deployments {
		if deployment == nil || len(deployment.ProtoReflect().GetUnknown()) != 0 {
			return bad("deployment fields")
		}
		key := deployment.HostId + "/" + deployment.ComponentId
		if _, ok := public[deployment.HostId]; !ok || placed[key] {
			return bad("deployment on an unknown host or duplicate deployment")
		}
		placed[key] = true
		deployments = append(deployments, servicecatalog.Deployment{HostID: deployment.HostId, ComponentID: deployment.ComponentId, Status: statusName(deployment.Enabled)})
	}
	topology := servicecatalog.NewTopology(hosts, deployments)
	compiled, err := catalog.Compile(topology, hostID)
	if err != nil {
		return bad("compile")
	}
	if err := compiled.Directory.Validate(); err != nil {
		return bad("directory")
	}
	compiled.Directory.Version = owned.Hash
	v := &View{hostID: hostID, raw: owned, compiled: compiled, routes: map[string]servicecatalog.Route{}, relays: map[string]servicecatalog.RelayTarget{}, topology: topology, entries: entries}
	for _, route := range compiled.Routes {
		v.routes[route.ServicePath+"/"+route.Method] = route
	}
	for _, relay := range compiled.Relays {
		v.relays[relay.HostID] = relay
	}
	// 校验公钥：每台登记了公钥的主机一把，连同部署在它上面、可以用它签名的调用方；加不属于主机的身份。
	// 身份名只认 moox-cli 与目录里的外部身份，别的名字忽略：否则登记成组件名的身份公钥会让持有它的人冒充组件。
	signing := catalog.SigningCallers(topology)
	keys, identities, named := map[string]gatewayauth.HostKey{}, map[string]ed25519.PublicKey{}, map[string]bool{}
	for id, key := range public {
		if len(key) != 0 {
			keys[id] = gatewayauth.HostKey{PublicKey: key, Callers: signing[id]}
		}
	}
	for _, identity := range owned.Identities {
		if identity == nil || len(identity.ProtoReflect().GetUnknown()) != 0 || len(identity.SignPublicKey) != ed25519.PublicKeySize {
			return bad("identity fields")
		}
		if named[identity.Identity] {
			return bad("duplicate identity")
		}
		named[identity.Identity] = true
		if identity.Identity == servicecatalog.OperatorMooxCLI || catalog.ExternalIdentity(identity.Identity) {
			identities[identity.Identity] = identity.SignPublicKey
		}
	}
	if v.keys, err = gatewayauth.NewKeys(keys, identities); err != nil {
		return bad("verification keys")
	}
	return v, nil
}

func statusName(enabled bool) string {
	if enabled {
		return servicecatalog.Enabled
	}
	return servicecatalog.Disabled
}
