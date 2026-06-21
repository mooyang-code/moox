package main

import "testing"

func TestControlGatewayRewrite(t *testing.T) {
	cases := []struct {
		path        string
		wantPath    string
		wantService string
		wantMethod  string
	}{
		{"/api/control/space/ListSpaces", "/gateway/space/ListSpaces", "space", "ListSpaces"},
		{"/api/control/cloudnode/ListNodes", "/gateway/cloudnode/ListNodes", "cloudnode", "ListNodes"},
	}
	for _, tc := range cases {
		got, ok := resolveControlGatewayTarget(tc.path)
		if !ok {
			t.Fatalf("%s was not recognized as control gateway path", tc.path)
		}
		if got.Path != tc.wantPath || got.Service != tc.wantService || got.Method != tc.wantMethod {
			t.Fatalf("%s => %+v, want path=%s service=%s method=%s", tc.path, got, tc.wantPath, tc.wantService, tc.wantMethod)
		}
	}
}

func TestStorageGatewayRewrite(t *testing.T) {
	cases := []struct {
		path       string
		wantBase   string
		wantRPC    string
		wantMethod string
	}{
		{"/api/storage/metadata/ListDatasets", "metadata", "/trpc.storage.metadata.MetadataService/ListDatasets", "ListDatasets"},
		{"/api/storage/access/ReadTimeSeriesRows", "access", "/trpc.storage.access.AccessService/ReadTimeSeriesRows", "ReadTimeSeriesRows"},
		{"/api/storage/view/QueryTimeSeriesRows", "view", "/trpc.storage.view.ViewService/QueryTimeSeriesRows", "QueryTimeSeriesRows"},
	}
	for _, tc := range cases {
		got, ok := resolveStorageGatewayTarget(tc.path)
		if !ok {
			t.Fatalf("%s was not recognized as storage gateway path", tc.path)
		}
		if got.Base != tc.wantBase || got.Path != tc.wantRPC || got.Method != tc.wantMethod {
			t.Fatalf("%s => %+v, want base=%s path=%s method=%s", tc.path, got, tc.wantBase, tc.wantRPC, tc.wantMethod)
		}
	}
}

func TestGatewayRewriteRejectsInvalidPaths(t *testing.T) {
	invalidControl := []string{"/api/control", "/api/control/space", "/api/control/space/List/Extra", "/gateway/space/ListSpaces"}
	for _, path := range invalidControl {
		if got, ok := resolveControlGatewayTarget(path); ok {
			t.Fatalf("%s unexpectedly resolved to %+v", path, got)
		}
	}

	invalidStorage := []string{"/api/storage", "/api/storage/device/ListDevices", "/api/storage/access", "/api/storage/view/Query/Extra"}
	for _, path := range invalidStorage {
		if got, ok := resolveStorageGatewayTarget(path); ok {
			t.Fatalf("%s unexpectedly resolved to %+v", path, got)
		}
	}
}
