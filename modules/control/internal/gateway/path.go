package gateway

import "strings"

const apiControlPathPrefix = "/api/control/"
const apiServicePathPrefix = "/api/service/"

func IsControlAPIPath(rpcName string) bool {
	return strings.HasPrefix(rpcName, apiControlPathPrefix)
}

func IsServiceAPIPath(rpcName string) bool {
	return strings.HasPrefix(rpcName, apiServicePathPrefix)
}
