package gatewayroute

import "testing"

func TestTableReplaceRejectsHashMismatchWithoutChangingCurrentSnapshot(t *testing.T) {
	good, err := NormalizeAndHash("node-1", []Route{{ServiceID: "admin", Address: "127.0.0.1:8080", ServicePath: "trpc.moox.Admin", AllowedMethods: []string{"*"}, AllowedCallers: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}
	var table Table
	if err := table.Replace(good); err != nil {
		t.Fatal(err)
	}
	bad := good
	bad.RouteHash = "not-the-hash"
	bad.Routes = []Route{{ServiceID: "storage", Address: "127.0.0.1:8081", ServicePath: "trpc.moox.Storage", AllowedMethods: []string{"SearchRecordRows"}, AllowedCallers: []string{"console"}}}
	if err := table.Replace(bad); err == nil {
		t.Fatal("Replace accepted a bad hash")
	}
	if _, _, ok := table.ResolveRPC("/trpc.moox.Admin/Login"); !ok {
		t.Fatal("failed Replace changed current snapshot")
	}
	if _, _, ok := table.ResolveRPC("/trpc.moox.Storage/SearchRecordRows"); ok {
		t.Fatal("failed Replace partially installed routes")
	}
}

func TestTableReplaceRejectsAnInvalidRouteWithoutChangingCurrentSnapshot(t *testing.T) {
	good, err := NormalizeAndHash("node-1", []Route{{ServiceID: "admin", Address: "127.0.0.1:8080", ServicePath: "trpc.moox.Admin", AllowedMethods: []string{"*"}, AllowedCallers: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}
	var table Table
	if err := table.Replace(good); err != nil {
		t.Fatal(err)
	}
	invalid := good
	invalid.Routes = append([]Route(nil), good.Routes...)
	invalid.Routes[0].Address = "192.0.2.1:8080"
	if err := table.Replace(invalid); err == nil {
		t.Fatal("Replace accepted a non-loopback route")
	}
	route, _, ok := table.ResolveRPC("/trpc.moox.Admin/Login")
	if !ok || route.Address != "127.0.0.1:8080" {
		t.Fatalf("failed Replace changed current route: %+v, %v", route, ok)
	}
}

func TestTableReplaceAndResolveAreImmutable(t *testing.T) {
	snapshot, err := NormalizeAndHash("node-1", []Route{{ServiceID: "admin", Address: "127.0.0.1:8080", ServicePath: "trpc.moox.Admin", AllowedMethods: []string{"*"}, AllowedCallers: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}
	var table Table
	if err := table.Replace(snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Routes[0].Address = "127.0.0.1:9999"
	route, _, ok := table.ResolveRPC("/trpc.moox.Admin/Login")
	if !ok || route.Address != "127.0.0.1:8080" {
		t.Fatalf("ResolveRPC = %+v, %v", route, ok)
	}
	route.Address = "127.0.0.1:7777"
	again, _, _ := table.ResolveRPC("/trpc.moox.Admin/Login")
	if again.Address != "127.0.0.1:8080" {
		t.Fatalf("ResolveRPC exposed live route: %+v", again)
	}
}

func TestTableAllowsEmptySnapshotAndHonorsDisabled(t *testing.T) {
	var table Table
	empty, err := NormalizeAndHash("node-1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Replace(empty); err != nil {
		t.Fatalf("Replace(empty): %v", err)
	}
	snapshot, err := NormalizeAndHashState("node-1", true, []Route{{ServiceID: "admin", Address: "127.0.0.1:8080", ServicePath: "trpc.moox.Admin", AllowedMethods: []string{"*"}, AllowedCallers: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := table.Replace(snapshot); err != nil {
		t.Fatal(err)
	}
	if _, _, ok := table.ResolveRPC("/trpc.moox.Admin/Login"); ok {
		t.Fatal("ResolveRPC returned a route from a disabled snapshot")
	}
}

func TestTableAcceptsStateAwareEnabledAndDisabledHashes(t *testing.T) {
	var table Table
	for _, disabled := range []bool{false, true} {
		snapshot, err := NormalizeAndHashState("node-1", disabled, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := table.Replace(snapshot); err != nil {
			t.Fatalf("Replace(disabled=%v): %v", disabled, err)
		}
	}
}

func TestTableDeepCopiesAllowedMethods(t *testing.T) {
	snapshot, err := NormalizeAndHash("node-1", []Route{{ServiceID: "sysdeploy", Address: "127.0.0.1:11109", ServicePath: "trpc.moox.ops.SysDeploy", AllowedMethods: []string{"ListHosts"}, AllowedCallers: []string{"*"}}})
	if err != nil {
		t.Fatal(err)
	}
	var table Table
	if err := table.Replace(snapshot); err != nil {
		t.Fatal(err)
	}
	snapshot.Routes[0].AllowedMethods[0] = "DeleteHost"
	route, _, ok := table.ResolveRPC("/trpc.moox.ops.SysDeploy/ListHosts")
	if !ok || !route.AllowsMethod("ListHosts") || route.AllowsMethod("DeleteHost") {
		t.Fatalf("table methods mutated: %+v", route)
	}
	route.AllowedMethods[0] = "CreateGatewayNode"
	again, _, _ := table.ResolveRPC("/trpc.moox.ops.SysDeploy/ListHosts")
	if !again.AllowsMethod("ListHosts") {
		t.Fatalf("resolved method slice was live: %+v", again)
	}
}

func TestTableResolveRPCUsesServicePathAndMethodAllowlist(t *testing.T) {
	snapshot, err := NormalizeAndHash("node-1", []Route{{
		ServiceID: "storage-primary", Address: "127.0.0.1:20102",
		ServicePath: "trpc.moox.storage.PrimaryStore", AllowedMethods: []string{"MergeRows"}, AllowedCallers: []string{"storage-view"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	var table Table
	if err := table.Replace(snapshot); err != nil {
		t.Fatal(err)
	}
	route, method, ok := table.ResolveRPC("/trpc.moox.storage.PrimaryStore/MergeRows")
	if !ok || route.ServiceID != "storage-primary" || method != "MergeRows" {
		t.Fatalf("ResolveRPC = %+v, %q, %v", route, method, ok)
	}
	if _, _, ok := table.ResolveRPC("/trpc.moox.storage.PrimaryStore/ReadRows"); ok {
		t.Fatal("ResolveRPC allowed a method outside the route allowlist")
	}
}

func TestTableResolveRPCForCallerSelectsDisjointNativeRoute(t *testing.T) {
	snapshot, err := NormalizeAndHash("node-1", []Route{
		{ServiceID: "trade", Address: "127.0.0.1:11200", ServicePath: "trpc.moox.trade.TradeConsoleService", AllowedMethods: []string{"GetLogicalAccount"}, AllowedCallers: []string{"console"}},
		{ServiceID: "trade-owner", Address: "127.0.0.1:11200", ServicePath: "trpc.moox.trade.TradeConsoleService", AllowedMethods: []string{"GetLogicalAccount"}, AllowedCallers: []string{"strategy"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	var table Table
	if err := table.Replace(snapshot); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ caller, service string }{{"console", "trade"}, {"strategy", "trade-owner"}} {
		route, method, ok := table.ResolveRPCForCaller("/trpc.moox.trade.TradeConsoleService/GetLogicalAccount", test.caller)
		if !ok || method != "GetLogicalAccount" || route.ServiceID != test.service {
			t.Fatalf("caller %q resolved route=%+v method=%q ok=%v", test.caller, route, method, ok)
		}
	}
	if _, _, ok := table.ResolveRPCForCaller("/trpc.moox.trade.TradeConsoleService/GetLogicalAccount", "unknown"); ok {
		t.Fatal("unknown caller resolved native route")
	}
}
