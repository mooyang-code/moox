package gateway

import "strings"

const apiAdminPathPrefix = "/api/admin/"
const apiServicePathPrefix = "/api/service/"

func IsAdminAPIPath(rpcName string) bool {
	return strings.HasPrefix(rpcName, apiAdminPathPrefix)
}

func IsServiceAPIPath(rpcName string) bool {
	return strings.HasPrefix(rpcName, apiServicePathPrefix)
}
