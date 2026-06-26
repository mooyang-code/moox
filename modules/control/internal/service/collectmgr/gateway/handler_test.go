package gateway

import "testing"

func TestParseMethodToRouteRecalculateAllTaskInstances(t *testing.T) {
	handler := &CollectorGatewayHandler{}

	route, err := handler.parseMethodToRoute("RecalculateAllTaskInstances", nil)
	if err != nil {
		t.Fatalf("parseMethodToRoute returned error: %v", err)
	}
	if route.HTTPMethod != "POST" {
		t.Fatalf("HTTPMethod = %q, want POST", route.HTTPMethod)
	}
	if route.Path != "/api/v1/task-planner/recalculate-all" {
		t.Fatalf("Path = %q, want /api/v1/task-planner/recalculate-all", route.Path)
	}
}
