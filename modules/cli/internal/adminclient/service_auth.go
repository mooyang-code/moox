package adminclient

import (
	"github.com/mooyang-code/moox/packages/serviceauth"
	"time"
)

type ServiceAuthConfig struct {
	Version, AccessKey, SecretKey string
	ExpireSecs                    int64
}

func (c ServiceAuthConfig) BuildAuthHeader(method, path string, body []byte, now time.Time) (string, error) {
	return serviceauth.BuildHeader(serviceauth.Config{AccessKey: c.AccessKey, SecretKey: c.SecretKey, ExpireSeconds: c.ExpireSecs}, serviceauth.Request{Method: method, Path: path, Body: body}, now)
}
