package adminclient

import (
	"github.com/mooyang-code/moox/packages/servicegateway"
	"time"
)

type ServiceAuthConfig struct {
	Version, AccessKey, SecretKey string
	ExpireSecs                    int64
}

func (c ServiceAuthConfig) BuildAuthHeader(method, path string, body []byte, headers map[string]string, now time.Time) (string, error) {
	return servicegateway.BuildHeader(servicegateway.AuthConfig{AccessKey: c.AccessKey, SecretKey: c.SecretKey, ExpireSeconds: c.ExpireSecs}, servicegateway.AuthRequest{Method: method, Path: path, Body: body, Headers: headers}, now)
}
