package cmd

import (
	"path/filepath"
	"runtime"
	"testing"
)

func TestMarketLocalRouteSeedCoversEveryBuiltinDataset(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
	routes, err := loadMetadataSeed(filepath.Join(root, "examples", "metadata-market-local-routes.seed.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	covered := make(map[string]struct{}, len(routes.PrimaryStoreRoutes))
	for _, route := range routes.PrimaryStoreRoutes {
		if route.NodeID != "local" || route.SubjectPattern != "*" {
			t.Fatalf("route %s/%s is not a local wildcard route", route.SpaceID, route.RouteID)
		}
		key := route.SpaceID + "\x00" + route.DatasetID
		if _, exists := covered[key]; exists {
			t.Fatalf("duplicate route for %s/%s", route.SpaceID, route.DatasetID)
		}
		covered[key] = struct{}{}
	}
	for _, marketID := range builtinMarketIDs {
		seed, err := loadMetadataSeed(filepath.Join(root, "modules", "collector", "config", "markets", marketID, "metadata.seed.yaml"))
		if err != nil {
			t.Fatal(err)
		}
		for _, dataset := range seed.Datasets {
			if _, exists := covered[marketID+"\x00"+dataset.DatasetID]; !exists {
				t.Fatalf("missing local route for %s/%s", marketID, dataset.DatasetID)
			}
		}
	}
}
