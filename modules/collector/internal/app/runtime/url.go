package runtime

import "fmt"

// URL returns the backend gateway endpoint used by collector runtime callbacks.
func URL(serverIP string, serverPort int, service string, method string) string {
	return fmt.Sprintf("http://%s:%d/api/service/%s/%s", serverIP, serverPort, service, method)
}

// ServiceURL returns the backend service endpoint using a full /api/service gateway target.
func ServiceURL(serviceGatewayTarget string, service string, method string) string {
	return fmt.Sprintf("%s/api/service/%s/%s", normalizeServiceGatewayTarget(serviceGatewayTarget), service, method)
}
