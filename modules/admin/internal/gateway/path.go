package gateway

import "strings"

const apiAdminPathPrefix = "/api/admin/"

func IsAdminAPIPath(rpcName string) bool {
	return strings.HasPrefix(rpcName, apiAdminPathPrefix)
}
