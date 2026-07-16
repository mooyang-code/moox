package adminclient

import (
	"net/http"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayauth"
)

type ServiceAuthConfig struct {
	AccessKey, SecretKey, TargetNode, CAFile string
	ExpireSecs                               int64
}

func (c ServiceAuthConfig) BuildAuthHeader(method, path string, body []byte, now time.Time) (http.Header, error) {
	return gatewayauth.Sign(gatewayauth.Credentials{KeyID: c.AccessKey, Secret: c.SecretKey, Expire: time.Duration(c.ExpireSecs) * time.Second}, gatewayauth.Request{Method: method, Path: path, Body: body, TargetNode: c.TargetNode}, now)
}
