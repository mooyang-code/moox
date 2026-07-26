package runtime

import "fmt"

// ServiceURL returns the backend service endpoint using a full /api/service gateway target.
func ServiceURL(serviceGatewayTarget string, service string, method string) string {
	return fmt.Sprintf("%s/api/service/%s/%s", normalizeServiceGatewayTarget(serviceGatewayTarget), service, method)
}
