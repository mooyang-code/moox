package jetstream

import "testing"

func TestConfigFromEnv(t *testing.T) {
	t.Setenv("MOOX_EVENTBUS_NATS_URL", "tls://eventbus-a:4222,eventbus-b:4222")
	t.Setenv("MOOX_EVENTBUS_NATS_USERNAME", "metrics")
	t.Setenv("MOOX_EVENTBUS_NATS_PASSWORD", "secret")
	t.Setenv("MOOX_EVENTBUS_NATS_TLS_CA_FILE", "/etc/moox/ca.pem")
	cfg := ConfigFromEnv([]string{"nats://ignored:4222"}, "test-client")
	if len(cfg.URLs) != 2 || cfg.URLs[0] != "tls://eventbus-a:4222" || cfg.Username != "metrics" || cfg.Password != "secret" || cfg.TLSCAFile != "/etc/moox/ca.pem" {
		t.Fatalf("ConfigFromEnv() = %+v", cfg)
	}
}
