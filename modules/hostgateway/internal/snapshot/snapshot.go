// Package snapshot validates and atomically publishes the complete host view.
// One request reads one immutable view for routing, identity and authorization.
package snapshot

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"sync/atomic"
	"time"

	pb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	directorypb "github.com/mooyang-code/moox/packages/gatewayroute/proto/gatewayroutegen"
	"github.com/mooyang-code/moox/packages/servicecatalog"
	"google.golang.org/protobuf/proto"
)

const MaxBytes = 16 << 20

var ErrInvalid = errors.New("invalid host gateway snapshot")

type State struct{ current atomic.Pointer[View] }

func (s *State) Load() *View   { return s.current.Load() }
func (s *State) Apply(v *View) { s.current.Store(v) }

type View struct {
	raw       *pb.HostGatewaySnapshot
	routes    map[string]servicecatalog.Route
	keys      *gatewayauth.Keys
	directory servicecatalog.Directory
	relays    map[string]servicecatalog.RelayTarget
}

func (v *View) String() string {
	return fmt.Sprintf("HostSnapshot{host=%s hash=%s routes=%d}", v.HostID(), v.Hash(), v.Count())
}
func (v *View) GoString() string { return v.String() }
func (v *View) HostID() string   { return v.raw.HostId }
func (v *View) Hash() string     { return v.raw.Hash }
func (v *View) Disabled() bool   { return v.raw.Disabled }
func (v *View) Count() int       { return len(v.routes) }
func (v *View) Proto() *pb.HostGatewaySnapshot {
	return proto.Clone(v.raw).(*pb.HostGatewaySnapshot)
}
func (v *View) Directory() servicecatalog.Directory { return v.directory.Clone() }

// Relay 返回本机（control）对一台出站主机的中转许可。
func (v *View) Relay(hostID string) (servicecatalog.RelayTarget, bool) {
	relay, ok := v.relays[hostID]
	return relay, ok
}

// OutboundHost 报告目录里该主机是否只能出站、且由本机中转。
func (v *View) OutboundHost(hostID string) bool {
	host, ok := v.directory.Hosts[hostID]
	return ok && host.Relay == v.HostID()
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

// Build 复制输入，哈希不符、路由与内嵌目录不一致或校验公钥格式不对时拒绝整份快照。
// 路由地址只能是目录里固定的本机回环端口。
func Build(hostID string, raw *pb.HostGatewaySnapshot) (*View, error) {
	bad := func(reason string) (*View, error) { return nil, fmt.Errorf("%w: %s", ErrInvalid, reason) }
	if !servicecatalog.ValidHostID(hostID) || raw == nil || raw.HostId != hostID || raw.SchemaVersion != 1 ||
		len(raw.Routes) > 10000 || len(raw.VerificationKeys) > 4096 || proto.Size(raw) > MaxBytes {
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
	dir, err := decodeDirectory(owned.Directory)
	if err != nil {
		return bad("directory")
	}
	_, enabled := dir.Hosts[hostID]
	if owned.Disabled == enabled || owned.Disabled && len(owned.Routes) != 0 {
		return bad("host enabled state")
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return nil, err
	}
	for path := range dir.Services {
		if _, ok := catalog.Service(path); !ok {
			return bad("unknown directory service")
		}
	}
	v := &View{raw: owned, routes: map[string]servicecatalog.Route{}, directory: dir, relays: map[string]servicecatalog.RelayTarget{}}
	if len(owned.Relays) > 1024 {
		return bad("too many relays")
	}
	for _, relay := range owned.Relays {
		if relay == nil || len(relay.ProtoReflect().GetUnknown()) != 0 {
			return bad("relay fields")
		}
		target := servicecatalog.RelayTarget{HostID: relay.HostId}
		for _, port := range relay.Ports {
			if port == nil || len(port.ProtoReflect().GetUnknown()) != 0 {
				return bad("relay port fields")
			}
			target.Ports = append(target.Ports, servicecatalog.RelayPort{Port: int(port.Port), Callers: slices.Clone(port.Callers)})
		}
		_, duplicate := v.relays[target.HostID]
		if duplicate || dir.Hosts[target.HostID].Relay != hostID || !catalog.ValidRelay(target) {
			return bad("relay differs from directory or catalog")
		}
		v.relays[target.HostID] = target
	}
	// 由本机中转的每台出站主机都必须有许可，否则调用方打不开中转流。
	for id, host := range dir.Hosts {
		if _, ok := v.relays[id]; host.Relay == hostID && !ok {
			return bad("outbound host without relay")
		}
	}
	for _, route := range owned.Routes {
		if route == nil || len(route.ProtoReflect().GetUnknown()) != 0 {
			return bad("route fields")
		}
		component, ok := catalog.Component(route.ComponentId)
		if !ok {
			return bad("route component")
		}
		var service servicecatalog.Service
		for _, candidate := range component.Services {
			if candidate.Path == route.ServicePath {
				service = candidate
			}
		}
		timeout, limit := service.TimeoutFor(route.Method), service.MaxBodyBytes
		if limit == 0 {
			limit = servicecatalog.DefaultMaxBodyBytes
		}
		if _, declared := service.Method(route.Method); service.Path == "" || !declared ||
			route.Address != net.JoinHostPort("127.0.0.1", strconv.Itoa(service.Port)) ||
			route.TimeoutMs != timeout || route.MaxBodyBytes != limit || route.ReadOnly != catalog.ReadOnly(service.Path, route.Method) ||
			!slices.Contains(dir.Services[service.Path], hostID) || len(route.Callers) == 0 || len(route.Callers) > 2048 {
			return bad("route differs from catalog or directory")
		}
		seen := map[string]bool{}
		for _, caller := range route.Callers {
			if !catalog.Allowed(caller, route.ServicePath, route.Method) || seen[caller] {
				return bad("route caller")
			}
			seen[caller] = true
		}
		key := route.ServicePath + "/" + route.Method
		if _, duplicate := v.routes[key]; duplicate {
			return bad("duplicate route")
		}
		v.routes[key] = servicecatalog.Route{ComponentID: route.ComponentId, ServicePath: route.ServicePath, Method: route.Method,
			Address: route.Address, TimeoutMS: route.TimeoutMs, MaxBodyBytes: route.MaxBodyBytes, ReadOnly: route.ReadOnly, Callers: slices.Clone(route.Callers)}
	}
	// 校验公钥：每台登记主机一把（连同部署在它上面的调用方），加不属于主机的身份。身份名只认 moox-cli 与目录里的外部身份，
	// 别的名字忽略：否则登记成组件名的身份公钥会让持有它的人冒充组件。
	hosts, identities := map[string]gatewayauth.HostKey{}, map[string]ed25519.PublicKey{}
	for _, key := range owned.VerificationKeys {
		if key == nil || len(key.ProtoReflect().GetUnknown()) != 0 || len(key.PublicKey) != ed25519.PublicKeySize || len(key.Callers) > 256 {
			return bad("verification key fields")
		}
		if key.HostId == "" {
			_, duplicate := identities[key.Identity]
			if duplicate || len(key.Callers) != 0 {
				return bad("verification identity")
			}
			if key.Identity == servicecatalog.OperatorMooxCLI || catalog.ExternalIdentity(key.Identity) {
				identities[key.Identity] = key.PublicKey
			}
			continue
		}
		if _, duplicate := hosts[key.HostId]; duplicate || key.Identity != "" || !servicecatalog.ValidHostID(key.HostId) {
			return bad("verification host")
		}
		hosts[key.HostId] = gatewayauth.HostKey{PublicKey: key.PublicKey, Callers: key.Callers}
	}
	if v.keys, err = gatewayauth.NewKeys(hosts, identities); err != nil {
		return bad("verification keys")
	}
	return v, nil
}

func decodeDirectory(raw *directorypb.ServiceDirectory) (servicecatalog.Directory, error) {
	d := servicecatalog.Directory{Hosts: map[string]servicecatalog.DirectoryHost{}, Services: map[string][]string{}}
	if raw == nil || len(raw.ProtoReflect().GetUnknown()) != 0 {
		return d, errors.New("missing or unknown directory")
	}
	d.Version = raw.Version
	for id, host := range raw.Hosts {
		if host == nil || len(host.ProtoReflect().GetUnknown()) != 0 {
			return d, errors.New("invalid directory host")
		}
		d.Hosts[id] = servicecatalog.DirectoryHost{Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region, Relay: host.Relay}
	}
	for path, hosts := range raw.Services {
		if hosts == nil || len(hosts.ProtoReflect().GetUnknown()) != 0 {
			return d, errors.New("invalid directory service")
		}
		d.Services[path] = append([]string{}, hosts.HostIds...)
	}
	return d, d.Validate()
}
