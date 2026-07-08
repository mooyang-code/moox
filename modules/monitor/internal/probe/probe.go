package probe

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
)

type Runner interface {
	Run(ctx context.Context, check domain.Check) domain.CheckResult
}

type MultiRunner struct {
	HTTP HTTPRunner
	TCP  TCPRunner
}

func (r MultiRunner) Run(ctx context.Context, check domain.Check) domain.CheckResult {
	switch check.Kind {
	case domain.CheckKindHTTP:
		return r.HTTP.Run(ctx, check)
	case domain.CheckKindTCP:
		return r.TCP.Run(ctx, check)
	default:
		result := baseResult(check, 0)
		result.Success = false
		result.Status = domain.CheckStatusDown
		result.ErrorMessage = fmt.Sprintf("unsupported check kind %q", check.Kind)
		return result
	}
}

func DefaultRunner() MultiRunner {
	return MultiRunner{}
}

func baseResult(check domain.Check, latency time.Duration) domain.CheckResult {
	return domain.CheckResult{
		ResultID:  newResultID(),
		SpaceID:   check.SpaceID,
		CheckID:   check.CheckID,
		Status:    domain.CheckStatusDown,
		LatencyMS: latency.Milliseconds(),
		CheckedAt: time.Now().UTC(),
	}
}

func checkTimeout(check domain.Check) time.Duration {
	if check.TimeoutMS <= 0 {
		return 3 * time.Second
	}
	return time.Duration(check.TimeoutMS) * time.Millisecond
}

func newResultID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("result-%d", time.Now().UnixNano())
	}
	return "result-" + hex.EncodeToString(b[:])
}

func failResult(check domain.Check, latency time.Duration, msg string) domain.CheckResult {
	result := baseResult(check, latency)
	result.Success = false
	result.Status = domain.CheckStatusDown
	result.ErrorMessage = msg
	return result
}
