package registry

import "testing"

func TestParseManifest(t *testing.T) {
	m, err := Parse("id: demo\nversion: 1.0.0\napi_version: moox.strategy/v1\nentrypoint: strategy.py:run\n")
	if err != nil || m.ID != "demo" {
		t.Fatalf("%+v %v", m, err)
	}
}
