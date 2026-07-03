package taskpublisher

import "testing"

func TestNewTrimsGatewayURL(t *testing.T) {
	client := New(Config{GatewayURL: "http://127.0.0.1:11000/"})
	if client.gatewayURL != "http://127.0.0.1:11000" {
		t.Fatalf("gatewayURL = %q", client.gatewayURL)
	}
}
