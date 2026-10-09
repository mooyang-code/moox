// Package snapshot validates and atomically publishes the complete host view.
// One request reads one immutable view for routing, identity and authorization.
package snapshot

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"

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

type verification struct {
	credentials gatewayauth.Credentials
	expires     int64
}

type View struct {
	raw       *pb.HostGatewaySnapshot
	routes    map[string]servicecatalog.Route
	keys      map[string]verification
	directory servicecatalog.Directory
}

// String/GoString deliberately exclude verification material.
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

func (v *View) Resolve(service, method string) (servicecatalog.Route, bool) {
	route, ok := v.routes[service+"/"+method]
	route.Callers = slices.Clone(route.Callers)
	return route, ok
}

func (v *View) Verify(request gatewayauth.Request, headers http.Header, now time.Time) (gatewayauth.Claims, error) {
	ids := headers.Values("X-Moox-Key-Id")
	if len(ids) != 1 {
		return gatewayauth.Claims{}, errors.New("host gateway authentication failed")
	}
	key, found := v.keys[ids[0]]
	if !found || key.expires > 0 && now.Unix() >= key.expires {
		return gatewayauth.Claims{}, errors.New("host gateway authentication failed")
	}
	claims, err := gatewayauth.Verify(key.credentials, request, headers, now)
	if err != nil {
		return gatewayauth.Claims{}, errors.New("host gateway authentication failed")
	}
	return claims, nil
}

// Build owns its input and rejects a mismatched hash, noncanonical routing or
// out-of-scope verification keys. Network addresses can never redirect RPCs
// away from the catalog's fixed loopback service port.
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
	v := &View{raw: owned, routes: map[string]servicecatalog.Route{}, keys: map[string]verification{}, directory: dir}
	callers := map[string]bool{}
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
		timeout, limit := service.TimeoutMS, service.MaxBodyBytes
		if timeout == 0 {
			timeout = servicecatalog.DefaultTimeoutMS
		}
		if limit == 0 {
			limit = servicecatalog.DefaultMaxBodyBytes
		}
		if service.Path == "" || !slices.Contains(service.Methods, route.Method) ||
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
			seen[caller], callers[caller] = true, true
		}
		key := route.ServicePath + "/" + route.Method
		if _, duplicate := v.routes[key]; duplicate {
			return bad("duplicate route")
		}
		v.routes[key] = servicecatalog.Route{ComponentID: route.ComponentId, ServicePath: route.ServicePath, Method: route.Method,
			Address: route.Address, TimeoutMS: route.TimeoutMs, MaxBodyBytes: route.MaxBodyBytes, ReadOnly: route.ReadOnly, Callers: slices.Clone(route.Callers)}
	}
	counts := map[string]int{}
	for _, key := range owned.VerificationKeys {
		if key == nil || len(key.ProtoReflect().GetUnknown()) != 0 || !callers[key.Caller] || !keyID(key.KeyId) ||
			len(key.Secret) < 32 || len(key.Secret) > 4096 || key.ExpiresAtUnix < 0 {
			return bad("verification key scope or fields")
		}
		if _, duplicate := v.keys[key.KeyId]; duplicate {
			return bad("duplicate key ID")
		}
		counts[key.Caller]++
		if counts[key.Caller] > 2 {
			return bad("more than two keys per caller")
		}
		v.keys[key.KeyId] = verification{credentials: gatewayauth.Credentials{Caller: key.Caller, KeyID: key.KeyId, Secret: string(key.Secret)}, expires: key.ExpiresAtUnix}
	}
	for caller := range callers {
		if counts[caller] == 0 {
			return bad("missing verification key")
		}
	}
	return v, nil
}

func keyID(id string) bool {
	return id != "" && len(id) <= 128 && !strings.ContainsAny(id, "/\\") &&
		!strings.ContainsFunc(id, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) })
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
		d.Hosts[id] = servicecatalog.DirectoryHost{Address: host.Address, PrivateAddress: host.PrivateAddress, Region: host.Region}
	}
	for path, hosts := range raw.Services {
		if hosts == nil || len(hosts.ProtoReflect().GetUnknown()) != 0 {
			return d, errors.New("invalid directory service")
		}
		d.Services[path] = append([]string{}, hosts.HostIds...)
	}
	return d, d.Validate()
}
