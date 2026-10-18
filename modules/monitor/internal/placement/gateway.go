package placement

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	adminpb "github.com/mooyang-code/moox/modules/admin/proto/admingen"
	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

type gatewaySource interface {
	GatewayStatus(context.Context, string) (*adminpb.HostGatewayRuntimeStatus, error)
}

func (s *Syncer) syncGateways(ctx context.Context, snapshot Snapshot) error {
	if s.gateways == nil {
		return nil
	}
	source, ok := s.source.(gatewaySource)
	if !ok {
		return fmt.Errorf("placement discovery source cannot read host gateway status")
	}
	observations := make([]domain.GatewayObservation, 0, len(snapshot.Hosts))
	var failures []error
	for _, host := range snapshot.Hosts {
		if err := ctx.Err(); err != nil {
			return err
		}
		if host.GetStatus() != servicecatalog.Enabled {
			continue
		}
		row := domain.GatewayObservation{HostID: host.GetHostId()}
		for _, value := range []string{host.GetCreatedAt(), host.GetUpdatedAt()} {
			if at, err := time.Parse(time.RFC3339Nano, value); err == nil && at.After(row.HostEnabledAt) {
				row.HostEnabledAt = at
			}
		}
		status, err := source.GatewayStatus(ctx, host.GetHostId())
		if err == nil && status == nil {
			err = fmt.Errorf("GetHostRoutes returned no gateway status")
		}
		if err == nil {
			for _, value := range []string{status.GetLastSeenAt(), status.GetConflictSeenAt(), status.GetReplacedAt()} {
				if value == "" {
					continue
				}
				if _, parseErr := time.Parse(time.RFC3339Nano, value); parseErr != nil {
					err = fmt.Errorf("invalid gateway timestamp: %w", parseErr)
					break
				}
			}
		}
		if err != nil {
			row.ReadError = err.Error()
			failures = append(failures, fmt.Errorf("gateway %s: %w", host.GetHostId(), err))
		} else {
			raw, marshalErr := json.Marshal(status)
			if marshalErr != nil {
				return marshalErr
			}
			row.StatusJSON, row.ExpectedHash, row.AppliedHash = string(raw), status.GetExpectedHash(), status.GetAppliedHash()
		}
		observations = append(observations, row)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.gateways.Reconcile(ctx, observations, s.now().UTC()); err != nil {
		return err
	}
	return errors.Join(failures...)
}
