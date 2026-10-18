package engine

import (
	"encoding/json"
	"fmt"
	"net"
	"time"

	"github.com/mooyang-code/moox/modules/consoleproxy/internal/config"
)

// Render builds only typed, constrained settings, never an arbitrary external
// Caddyfile. It does not call Caddy provisioning or touch persistent state.
func Render(c config.Config) ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	type obj = map[string]any
	issuer := obj{"module": "internal", "ca": "local"}
	if c.TLS.Mode == "public" {
		issuer = obj{"module": "acme", "email": c.TLS.Email, "challenges": obj{"tls-alpn": obj{"disabled": true}}}
		if c.TLS.ACMECA != "" {
			issuer["ca"] = c.TLS.ACMECA
		}
	}
	proxy := func(addr string) obj {
		return obj{"handler": "reverse_proxy", "upstreams": []obj{{"dial": addr}}, "stream_close_delay": 0}
	}
	routes := []obj{
		{"handle": []obj{{"handler": "moox_console_admission"}}},
		{"handle": []obj{{"handler": "headers", "response": obj{"set": obj{
			"X-Content-Type-Options": []string{"nosniff"}, "X-Frame-Options": []string{"DENY"}, "Referrer-Policy": []string{"no-referrer"},
			"Content-Security-Policy": []string{"default-src 'self'; script-src 'self' 'unsafe-eval'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; connect-src 'self' https: wss:; font-src 'self' data:; object-src 'none'; base-uri 'self'; frame-ancestors 'none'"},
		}}}}},
		{"handle": []obj{{"handler": "encode", "encodings": obj{"zstd": obj{}, "gzip": obj{}}, "prefer": []string{"zstd", "gzip"}}}},
		{"match": []obj{{"path": []string{"/api/admin/*"}}}, "handle": []obj{proxy(c.Upstreams.Admin)}, "terminal": true},
		{"match": []obj{{"path": []string{"/api", "/api/*", "/healthz", "/healthz/*", "/readyz", "/readyz/*", "/metrics", "/metrics/*"}}}, "handle": []obj{{"handler": "static_response", "status_code": 404}}, "terminal": true},
		{"handle": []obj{proxy(c.Upstreams.Web)}, "terminal": true},
	}
	protocols := []string{"h1", "h2"}
	if c.Public.HTTP3 {
		protocols = append(protocols, "h3")
	}
	apps := obj{
		"tls": obj{"automation": obj{"policies": []obj{{"subjects": []string{c.Public.Host}, "issuers": []obj{issuer}}}}},
		"http": obj{"grace_period": int64(c.Lifecycle.EngineStopTimeout), "servers": obj{"console": obj{
			"listen":    []string{net.JoinHostPort(c.Public.Bind, fmt.Sprint(c.Public.Port))},
			"protocols": protocols, "read_header_timeout": int64(10 * time.Second), "idle_timeout": int64(2 * time.Minute),
			"tls_connection_policies": []obj{{"default_sni": c.Public.Host}},
			"automatic_https":         obj{"disable_redirects": true},
			"routes": []obj{
				{"match": []obj{{"host": []string{c.Public.Host}}}, "handle": []obj{{"handler": "subroute", "routes": routes}}, "terminal": true},
				{"handle": []obj{{"handler": "static_response", "status_code": 404}}, "terminal": true},
			},
		}}},
	}
	if c.TLS.Mode == "internal" {
		// Instantiating PKI in public mode would create an unused internal CA.
		// Whenever it is needed, explicitly disable trust-store installation.
		apps["pki"] = obj{"certificate_authorities": obj{"local": obj{"install_trust": false}}}
	}
	return json.Marshal(obj{
		"admin": obj{"disabled": true, "config": obj{"persist": false}},
		"logging": obj{"logs": obj{"default": obj{"level": "INFO", "encoder": obj{
			"format": "filter", "wrap": obj{"format": "json"}, "fields": obj{
				"request>uri": obj{"filter": "delete"}, "request>headers": obj{"filter": "delete"},
				"resp_headers": obj{"filter": "delete"},
			},
		}}}},
		"storage": obj{"module": "file_system", "root": c.TLS.StorageRoot},
		"apps":    apps,
	})
}
