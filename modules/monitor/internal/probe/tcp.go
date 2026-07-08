package probe

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
)

type TCPRunner struct{}

func (r TCPRunner) Run(ctx context.Context, check domain.Check) domain.CheckResult {
	timeout := checkTimeout(check)
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	addr := fmt.Sprintf("%s:%d", check.TCPHost, check.TCPPort)
	start := time.Now()
	conn, err := (&net.Dialer{Timeout: timeout}).DialContext(reqCtx, "tcp", addr)
	latency := time.Since(start)
	if err != nil {
		return failResult(check, latency, err.Error())
	}
	_ = conn.Close()

	result := baseResult(check, latency)
	result.Connected = true
	if check.MaxResponseMS > 0 && latency.Milliseconds() > int64(check.MaxResponseMS) {
		result.Success = false
		result.Status = domain.CheckStatusDegraded
		result.ErrorMessage = fmt.Sprintf("connect time %dms exceeds %dms", latency.Milliseconds(), check.MaxResponseMS)
		return result
	}
	result.Success = true
	result.Status = domain.CheckStatusOK
	return result
}
