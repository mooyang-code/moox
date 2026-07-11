package providers

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/marketdata"
)

type RequestGate interface {
	BeforeRequest(context.Context, RequestMeta) (RequestPermit, error)
}
type RequestMeta struct {
	ProviderID     marketdata.ProviderID
	JobItemID      string
	AttemptNo      int
	ExecutionNonce string
	RequestIndex   int
	EndpointClass  string
	QuotaScopeKey  string
	RequestCost    int64
}
type RequestPermit struct {
	PermitID     string
	LeaseEpoch   int64
	Allowed      bool
	NotBefore    time.Time
	ExpiresAt    time.Time
	DenialReason string
}
type StaticGate struct {
	Permit RequestPermit
	Err    error
}

func (g StaticGate) BeforeRequest(context.Context, RequestMeta) (RequestPermit, error) {
	return g.Permit, g.Err
}
