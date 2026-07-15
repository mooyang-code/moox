package gatewayproxy

import (
	"errors"
	"time"
)

var (
	ErrGatewayNodeNotFound = errors.New("gateway node not found")
	ErrInvalidGatewayRoute = errors.New("invalid gateway route")
)

type GatewayStatusReport struct {
	NodeID           string
	AppliedRouteHash string
	RouteCount       int32
	LastSeenAt       time.Time
	LastError        string
}
