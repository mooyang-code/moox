package tdx

import "testing"

func TestLoadEndpointsRejectsWrongPortAndMisspelledPortField(t *testing.T) {
	if _, err := LoadEndpoints([]byte(`[{"name":"normal","host":"192.0.2.1","port":7727,"variant":"tdx_normal"}]`)); err == nil {
		t.Fatal("expected normal endpoint port validation error")
	}
	if _, err := LoadEndpoints([]byte(`[{"name":"normal","host":"192.0.2.1","ort":7709,"variant":"tdx_normal"}]`)); err == nil {
		t.Fatal("expected missing port validation error")
	}
}
