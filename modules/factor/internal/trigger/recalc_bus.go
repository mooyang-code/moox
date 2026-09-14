package trigger

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
)

const (
	RecalcClaimSubject     = "moox.factor.internal.recalc.claim"
	RecalcReportSubject    = "moox.factor.internal.recalc.report"
	RecalcHeartbeatSubject = "moox.factor.internal.recalc.heartbeat"
)

type RecalcBus interface {
	Subscribe(string, nats.MsgHandler) (*nats.Subscription, error)
	RequestWithContext(context.Context, string, []byte) (*nats.Msg, error)
	FlushWithContext(context.Context) error
	MaxPayload() int64
}

type recalcWireError struct {
	Error string `json:"error,omitempty"`
}

type recalcClaimResponse struct {
	Job   *RecalcJob `json:"job,omitempty"`
	Error string     `json:"error,omitempty"`
}

type recalcReportRequest struct {
	JobID        string `json:"job_id"`
	Status       string `json:"status"`
	FailureClass string `json:"failure_class,omitempty"`
	Error        string `json:"error,omitempty"`
}

type recalcHeartbeatRequest struct {
	EngineID string `json:"engine_id"`
	Desired  int64  `json:"desired_revision"`
	Applied  int64  `json:"applied_revision"`
}

func ServeRecalcQueue(ctx context.Context, bus RecalcBus, svc *RecalcService) ([]*nats.Subscription, error) {
	if bus == nil || svc == nil {
		return nil, fmt.Errorf("recalc queue requires a bus and service")
	}
	claim, err := bus.Subscribe(RecalcClaimSubject, func(message *nats.Msg) {
		if message.Reply == "" {
			return
		}
		response := recalcClaimResponse{}
		if len(message.Data) != 0 {
			response.Error = "invalid_request"
		} else {
			requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			job, found, claimErr := svc.ClaimNext(requestCtx)
			cancel()
			if claimErr != nil {
				response.Error = "claim_failed"
			} else if found {
				copied := job
				response.Job = &copied
			}
		}
		respondRecalc(bus, message, response)
	})
	if err != nil {
		return nil, err
	}
	report, err := bus.Subscribe(RecalcReportSubject, func(message *nats.Msg) {
		if message.Reply == "" {
			return
		}
		response := recalcWireError{}
		var req recalcReportRequest
		if unmarshalErr := json.Unmarshal(message.Data, &req); unmarshalErr != nil || strings.TrimSpace(req.JobID) == "" {
			response.Error = "invalid_request"
		} else {
			requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if applyErr := svc.ApplyResult(requestCtx, req.JobID, req.Status, req.FailureClass, req.Error); applyErr != nil {
				response.Error = applyErr.Error()
			}
			cancel()
		}
		respondRecalc(bus, message, response)
	})
	if err != nil {
		_ = claim.Unsubscribe()
		return nil, err
	}
	heartbeat, err := bus.Subscribe(RecalcHeartbeatSubject, func(message *nats.Msg) {
		if message.Reply == "" {
			return
		}
		response := recalcWireError{}
		var req recalcHeartbeatRequest
		if unmarshalErr := json.Unmarshal(message.Data, &req); unmarshalErr != nil || strings.TrimSpace(req.EngineID) == "" {
			response.Error = "invalid_request"
		} else {
			requestCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			if beatErr := svc.Heartbeat(requestCtx, req.EngineID, req.Desired, req.Applied); beatErr != nil {
				response.Error = beatErr.Error()
			}
			cancel()
		}
		respondRecalc(bus, message, response)
	})
	if err != nil {
		_ = claim.Unsubscribe()
		_ = report.Unsubscribe()
		return nil, err
	}
	flushCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err = bus.FlushWithContext(flushCtx); err != nil {
		_ = claim.Unsubscribe()
		_ = report.Unsubscribe()
		_ = heartbeat.Unsubscribe()
		return nil, err
	}
	return []*nats.Subscription{claim, report, heartbeat}, nil
}

func ClaimRecalcJob(ctx context.Context, bus RecalcBus) (RecalcJob, bool, error) {
	payload, err := requestRecalc(ctx, bus, RecalcClaimSubject, nil)
	if err != nil {
		return RecalcJob{}, false, err
	}
	var response recalcClaimResponse
	if err := json.Unmarshal(payload, &response); err != nil {
		return RecalcJob{}, false, fmt.Errorf("decode recalc claim: %w", err)
	}
	if response.Error != "" {
		return RecalcJob{}, false, fmt.Errorf("recalc claim failed: %s", response.Error)
	}
	if response.Job == nil {
		return RecalcJob{}, false, nil
	}
	return *response.Job, true, nil
}

func ReportRecalcJob(ctx context.Context, bus RecalcBus, jobID, status, failure, message string) error {
	payload, err := json.Marshal(recalcReportRequest{JobID: jobID, Status: status, FailureClass: failure, Error: message})
	if err != nil {
		return err
	}
	raw, err := requestRecalc(ctx, bus, RecalcReportSubject, payload)
	if err != nil {
		return err
	}
	return decodeRecalcWireError(raw)
}

func ReportRecalcHeartbeat(ctx context.Context, bus RecalcBus, engineID string, desired, applied int64) error {
	payload, err := json.Marshal(recalcHeartbeatRequest{EngineID: engineID, Desired: desired, Applied: applied})
	if err != nil {
		return err
	}
	raw, err := requestRecalc(ctx, bus, RecalcHeartbeatSubject, payload)
	if err != nil {
		return err
	}
	return decodeRecalcWireError(raw)
}

func requestRecalc(ctx context.Context, bus RecalcBus, subject string, payload []byte) ([]byte, error) {
	if bus == nil {
		return nil, fmt.Errorf("recalc client requires a NATS connection")
	}
	message, err := bus.RequestWithContext(ctx, subject, payload)
	if err != nil {
		return nil, err
	}
	return message.Data, nil
}

func decodeRecalcWireError(raw []byte) error {
	var response recalcWireError
	if err := json.Unmarshal(raw, &response); err != nil {
		return fmt.Errorf("decode recalc response: %w", err)
	}
	if response.Error != "" {
		return fmt.Errorf("recalc request failed: %s", response.Error)
	}
	return nil
}

func respondRecalc(bus RecalcBus, message *nats.Msg, value any) {
	payload, err := json.Marshal(value)
	if err != nil {
		payload = []byte(`{"error":"encoding_failed"}`)
	}
	if bus != nil && int64(len(payload)) > bus.MaxPayload() {
		payload = []byte(`{"error":"response_too_large"}`)
	}
	_ = message.Respond(payload)
}
