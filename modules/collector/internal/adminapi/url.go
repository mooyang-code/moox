package adminapi

import "fmt"

// URL returns the backend gateway endpoint used by collector runtime callbacks.
func URL(serverIP string, serverPort int, service string, method string) string {
	return fmt.Sprintf("http://%s:%d/api/service/%s/%s", serverIP, serverPort, service, method)
}
