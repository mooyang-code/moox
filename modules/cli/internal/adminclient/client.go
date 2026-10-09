package adminclient

import (
	"strings"
	"sync"
)

// Client borrows the command-owned SSH gateway.
type Client struct {
	Gateway        GatewayForwarder
	SpaceID        string
	publishLeaseMu sync.RWMutex
	publishLease   *CollectorPublishLease
}

type retInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func isRetInfoSuccess(code int) bool {
	return code == 0
}

func isRetInfoNotFound(info retInfo) bool {
	if info.Code == 404 {
		return true
	}
	message := strings.ToLower(strings.TrimSpace(info.Msg))
	return strings.Contains(message, "not found") || strings.Contains(message, "record not found")
}
