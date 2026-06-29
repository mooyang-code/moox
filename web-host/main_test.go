package main

import "testing"

func TestIsAPIRequest(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/api", true},
		{"/api/admin/auth/Login", true},
		{"/api/service/collectmgr/GetTaskRuleList", true},
		{"/assets/app.js", false},
		{"/settings/spaces", false},
	}

	for _, tc := range cases {
		if got := isAPIRequest(tc.path); got != tc.want {
			t.Fatalf("isAPIRequest(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestEnvOr(t *testing.T) {
	t.Setenv("MOOX_WEB_HOST_ADDR", ":18080")
	if got := envOr("MOOX_WEB_HOST_ADDR", ":10080"); got != ":18080" {
		t.Fatalf("envOr returned %q, want :18080", got)
	}

	if got := envOr("MOOX_WEB_HOST_MISSING", ":10080"); got != ":10080" {
		t.Fatalf("envOr returned %q, want :10080", got)
	}
}

func TestIsStaticAsset(t *testing.T) {
	if !isStaticAsset("/static/js/app.js") {
		t.Fatal("expected js file to be treated as static asset")
	}
	if isStaticAsset("/settings/spaces") {
		t.Fatal("expected SPA route to be treated as non-static")
	}
}
