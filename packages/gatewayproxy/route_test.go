package gatewayproxy

import (
	"testing"
	"time"
)

func TestValidateRouteAcceptsLiteralLoopbackAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8080", "[::1]:65535"} {
		route := Route{ServiceID: "storage_api", Address: address, ServicePath: "trpc.moox.storage.Storage", TimeoutMS: 1, MaxBodyBytes: 1}
		if err := ValidateRoute(route); err != nil {
			t.Fatalf("ValidateRoute(%q): %v", address, err)
		}
	}
}

func TestValidateRouteRejectsUnsafeRoutes(t *testing.T) {
	valid := Route{ServiceID: "storage-api", Address: "127.0.0.1:8080", ServicePath: "trpc.moox.storage.Storage", TimeoutMS: 1, MaxBodyBytes: 1}
	tests := []struct {
		name string
		edit func(*Route)
	}{
		{name: "empty service ID", edit: func(r *Route) { r.ServiceID = "" }},
		{name: "uppercase service ID", edit: func(r *Route) { r.ServiceID = "Storage" }},
		{name: "service ID slash", edit: func(r *Route) { r.ServiceID = "../storage" }},
		{name: "localhost name", edit: func(r *Route) { r.Address = "localhost:8080" }},
		{name: "generic domain", edit: func(r *Route) { r.Address = "example.com:8080" }},
		{name: "remote IPv4", edit: func(r *Route) { r.Address = "192.0.2.1:8080" }},
		{name: "unspecified IPv4", edit: func(r *Route) { r.Address = "0.0.0.0:8080" }},
		{name: "unspecified IPv6", edit: func(r *Route) { r.Address = "[::]:8080" }},
		{name: "mapped loopback", edit: func(r *Route) { r.Address = "[::ffff:127.0.0.1]:8080" }},
		{name: "IPv6 zone", edit: func(r *Route) { r.Address = "[::1%lo0]:8080" }},
		{name: "URL", edit: func(r *Route) { r.Address = "http://127.0.0.1:8080" }},
		{name: "missing port", edit: func(r *Route) { r.Address = "127.0.0.1" }},
		{name: "zero port", edit: func(r *Route) { r.Address = "127.0.0.1:0" }},
		{name: "signed port", edit: func(r *Route) { r.Address = "127.0.0.1:+80" }},
		{name: "large port", edit: func(r *Route) { r.Address = "127.0.0.1:65536" }},
		{name: "empty service path", edit: func(r *Route) { r.ServicePath = "" }},
		{name: "leading slash", edit: func(r *Route) { r.ServicePath = "/trpc.moox.Storage" }},
		{name: "path traversal", edit: func(r *Route) { r.ServicePath = "trpc.moox..Storage" }},
		{name: "nonpositive timeout", edit: func(r *Route) { r.TimeoutMS = -1 }},
		{name: "excessive timeout", edit: func(r *Route) { r.TimeoutMS = 120001 }},
		{name: "nonpositive body limit", edit: func(r *Route) { r.MaxBodyBytes = -1 }},
		{name: "excessive body limit", edit: func(r *Route) { r.MaxBodyBytes = 64<<20 + 1 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			route := valid
			test.edit(&route)
			if err := ValidateRoute(route); err == nil {
				t.Fatalf("ValidateRoute(%+v) succeeded", route)
			}
		})
	}
}

func TestValidateRouteAcceptsCapsAndRejectsValuesAboveCaps(t *testing.T) {
	base := Route{ServiceID: "admin", Address: "127.0.0.1:8080", ServicePath: "trpc.moox.Admin", TimeoutMS: maxTimeoutMS, MaxBodyBytes: maxMaxBodyBytes}
	if err := ValidateRoute(base); err != nil {
		t.Fatalf("ValidateRoute at caps: %v", err)
	}
	for _, test := range []struct {
		name string
		edit func(*Route)
	}{
		{name: "timeout above cap", edit: func(route *Route) { route.TimeoutMS = maxTimeoutMS + 1 }},
		{name: "body above cap", edit: func(route *Route) { route.MaxBodyBytes = maxMaxBodyBytes + 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			route := base
			test.edit(&route)
			if err := ValidateRoute(route); err == nil {
				t.Fatalf("ValidateRoute(%+v) succeeded", route)
			}
		})
	}
}

func TestNormalizeAndHashAppliesDefaultsSortsAndIsStable(t *testing.T) {
	routes := []Route{
		{ServiceID: "storage", Address: "127.0.0.1:8002", ServicePath: "trpc.moox.Storage"},
		{ServiceID: "admin", Address: "[::1]:8001", ServicePath: "trpc.moox.Admin"},
	}
	first, err := NormalizeAndHash("node-1", routes)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NormalizeAndHash("node-1", []Route{routes[1], routes[0]})
	if err != nil {
		t.Fatal(err)
	}
	if first.RouteHash == "" || first.RouteHash != second.RouteHash {
		t.Fatalf("unstable hashes: %q and %q", first.RouteHash, second.RouteHash)
	}
	if first.Routes[0].ServiceID != "admin" || first.Routes[0].TimeoutMS != 5000 || first.Routes[0].MaxBodyBytes != 4<<20 {
		t.Fatalf("unexpected normalized routes: %+v", first.Routes)
	}
	if first.GeneratedAt.IsZero() || time.Since(first.GeneratedAt) > time.Minute {
		t.Fatalf("unexpected generated time: %v", first.GeneratedAt)
	}
	routes[0].Address = "127.0.0.1:9999"
	if first.Routes[1].Address == routes[0].Address {
		t.Fatal("NormalizeAndHash retained caller-owned route storage")
	}
}

func TestCanonicalSnapshotHashExcludesGeneratedAt(t *testing.T) {
	normalized, err := NormalizeAndHash("node-1", []Route{{ServiceID: "admin", Address: "127.0.0.1:8080", ServicePath: "trpc.moox.Admin"}})
	if err != nil {
		t.Fatal(err)
	}
	first := normalized
	first.GeneratedAt = time.Unix(1, 0).UTC()
	second := normalized
	second.GeneratedAt = time.Unix(2, 0).UTC()
	firstHash, err := hashSnapshot(first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := hashSnapshot(second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHash != secondHash || firstHash != normalized.RouteHash {
		t.Fatalf("hashes = %q, %q, normalized %q", firstHash, secondHash, normalized.RouteHash)
	}
}

func TestNormalizeAndHashRejectsDuplicateServiceIDs(t *testing.T) {
	route := Route{ServiceID: "admin", Address: "127.0.0.1:8080", ServicePath: "trpc.moox.Admin"}
	if _, err := NormalizeAndHash("node-1", []Route{route, route}); err == nil {
		t.Fatal("NormalizeAndHash accepted duplicate service IDs")
	}
}

func TestNormalizeAndHashStateIncludesDisabledInHash(t *testing.T) {
	enabled, err := NormalizeAndHashState("node-1", false, nil)
	if err != nil {
		t.Fatal(err)
	}
	disabled, err := NormalizeAndHashState("node-1", true, nil)
	if err != nil {
		t.Fatal(err)
	}
	if enabled.RouteHash == disabled.RouteHash {
		t.Fatalf("enabled and disabled hashes match: %s", enabled.RouteHash)
	}
	if enabled.Disabled || !disabled.Disabled {
		t.Fatalf("disabled state not retained: enabled=%v disabled=%v", enabled.Disabled, disabled.Disabled)
	}
}

func TestNormalizeAllowedMethodsSortsDedupesHashesAndAuthorizesExactly(t *testing.T) {
	route := Route{ServiceID: "sysdeploy", Address: "127.0.0.1:11109", ServicePath: "trpc.moox.ops.SysDeploy", AllowedMethods: []string{"ListActiveServiceDeployments", "GetGatewayNodeRoutes", "ListActiveServiceDeployments"}}
	snapshot, err := NormalizeAndHash("node-1", []Route{route})
	if err != nil {
		t.Fatal(err)
	}
	got := snapshot.Routes[0]
	if len(got.AllowedMethods) != 2 || got.AllowedMethods[0] != "GetGatewayNodeRoutes" || got.AllowedMethods[1] != "ListActiveServiceDeployments" {
		t.Fatalf("allowed methods = %v", got.AllowedMethods)
	}
	if !got.AllowsMethod("ListActiveServiceDeployments") || got.AllowsMethod("CreateGatewayNode") {
		t.Fatalf("unexpected authorization: %+v", got)
	}
	unrestricted := Route{}
	if !unrestricted.AllowsMethod("Anything") {
		t.Fatal("empty allowlist should allow ordinary routes")
	}
	without, err := NormalizeAndHash("node-1", []Route{{ServiceID: route.ServiceID, Address: route.Address, ServicePath: route.ServicePath}})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.RouteHash == without.RouteHash {
		t.Fatal("allowed methods missing from hash")
	}
	route.AllowedMethods[0] = "Mutated"
	if snapshot.Routes[0].AllowedMethods[1] != "ListActiveServiceDeployments" {
		t.Fatal("normalization retained caller method storage")
	}
}

func TestNormalizeAllowedMethodsRejectsUnsafeNames(t *testing.T) {
	for _, method := range []string{"", "../Delete", "Service.Method", "With/Slash", "has space"} {
		_, err := NormalizeAndHash("node-1", []Route{{ServiceID: "svc", Address: "127.0.0.1:1", ServicePath: "trpc.test.Service", AllowedMethods: []string{method}}})
		if err == nil {
			t.Fatalf("unsafe method %q accepted", method)
		}
	}
}
