package peer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/store"
	monitorpb "github.com/mooyang-code/moox/modules/monitor/proto/monitorgen"
	"github.com/mooyang-code/moox/packages/commonpb"
	"github.com/mooyang-code/moox/packages/gatewayauth"
	"google.golang.org/protobuf/encoding/protojson"
	"gorm.io/gorm"
)

const peerSnapshotPath = "/api/service/monitor/GetPeerSnapshot"
const maxPeerSnapshotBytes int64 = 4 << 20
const maxPeerObservationSkew = 5 * time.Minute

type Remote struct {
	InstanceID string
	GatewayURL string
	NodeID     string
}

type PullerOptions struct {
	Peers           []Remote
	Timeout         time.Duration
	Credentials     gatewayauth.Credentials
	CAFile          string
	Alerts          *store.AlertRepository
	OwnerInstanceID string
}

type Puller struct {
	repo            *store.PeerRepository
	peers           []Remote
	timeout         time.Duration
	credentials     gatewayauth.Credentials
	client          *http.Client
	alerts          *store.AlertRepository
	ownerInstanceID string
}

func NewPuller(repo *store.PeerRepository, opts PullerOptions) (*Puller, error) {
	if repo == nil {
		return nil, errors.New("peer repository is required")
	}
	if strings.TrimSpace(opts.Credentials.KeyID) == "" || strings.TrimSpace(opts.Credentials.Secret) == "" {
		return nil, errors.New("peer gateway key_id and secret_key are required")
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client, err := gatewayauth.NewHTTPClient(gatewayauth.ClientOptions{Timeout: timeout, CAFile: opts.CAFile})
	if err != nil {
		return nil, fmt.Errorf("create peer gateway client: %w", err)
	}
	return &Puller{repo: repo, peers: append([]Remote(nil), opts.Peers...), timeout: timeout, credentials: opts.Credentials, client: client, alerts: opts.Alerts, ownerInstanceID: strings.TrimSpace(opts.OwnerInstanceID)}, nil
}

func (p *Puller) PullOnce(ctx context.Context) error {
	var joined error
	for _, remote := range p.peers {
		if err := p.pullRemote(ctx, remote); err != nil {
			joined = errors.Join(joined, fmt.Errorf("pull peer %s: %w", remote.InstanceID, err))
		}
	}
	return joined
}

func (p *Puller) MarkStale(ctx context.Context, now time.Time, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = p.timeout
	}
	transitioned, err := p.repo.MarkStaleTransitions(ctx, now.Add(-3*timeout))
	if err != nil {
		return err
	}
	for _, instanceID := range transitioned {
		if err := p.recordAvailabilityTransition(ctx, instanceID, false, now.UTC()); err != nil {
			return err
		}
	}
	return nil
}

func (p *Puller) pullRemote(ctx context.Context, remote Remote) error {
	if strings.TrimSpace(remote.InstanceID) == "" || strings.TrimSpace(remote.GatewayURL) == "" || strings.TrimSpace(remote.NodeID) == "" {
		return errors.New("peer instance_id, gateway_url, and node_id are required")
	}
	body, err := protojson.Marshal(&monitorpb.GetPeerSnapshotReq{})
	if err != nil {
		return fmt.Errorf("marshal peer snapshot request: %w", err)
	}
	endpoint := strings.TrimRight(remote.GatewayURL, "/") + peerSnapshotPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	auth, err := gatewayauth.Sign(p.credentials, gatewayauth.Request{
		Method: http.MethodPost, Path: req.URL.EscapedPath(), TargetNode: remote.NodeID, Body: body,
	}, time.Now())
	if err != nil {
		return fmt.Errorf("sign peer gateway request: %w", err)
	}
	for name, values := range auth {
		req.Header[name] = append([]string(nil), values...)
	}
	rsp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer rsp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(rsp.Body, maxPeerSnapshotBytes+1))
	if err != nil {
		return fmt.Errorf("read peer snapshot response: %w", err)
	}
	if int64(len(raw)) > maxPeerSnapshotBytes {
		return errors.New("peer snapshot response exceeds 4 MiB")
	}
	if rsp.StatusCode < 200 || rsp.StatusCode >= 300 {
		return fmt.Errorf("peer %s returned HTTP %d", remote.InstanceID, rsp.StatusCode)
	}
	var snapshot monitorpb.GetPeerSnapshotRsp
	if err := protojson.Unmarshal(raw, &snapshot); err != nil {
		return fmt.Errorf("decode peer snapshot response: %w", err)
	}
	if snapshot.GetRetInfo() == nil {
		return fmt.Errorf("peer %s RPC response is missing ret_info", remote.InstanceID)
	}
	if snapshot.GetRetInfo().GetCode() != commonpb.ErrorCode_SUCCESS {
		return fmt.Errorf("peer %s RPC failed: %s", remote.InstanceID, snapshot.GetRetInfo().GetMsg())
	}
	instanceID := snapshot.GetInstanceId()
	if instanceID == "" {
		instanceID = remote.InstanceID
	} else if instanceID != remote.InstanceID {
		return fmt.Errorf("peer instance_id %q does not match configured %q", instanceID, remote.InstanceID)
	}
	baseURL := snapshot.GetBaseUrl()
	if baseURL == "" {
		baseURL = remote.GatewayURL
	}
	receivedAt := time.Now().UTC()
	observedAt := receivedAt
	if snapshot.GetObservedAt() != "" {
		observedAt, err = time.Parse(time.RFC3339Nano, snapshot.GetObservedAt())
		if err != nil {
			return fmt.Errorf("parse peer observed_at: %w", err)
		}
		observedAt = observedAt.UTC()
		if observedAt.Before(receivedAt.Add(-maxPeerObservationSkew)) || observedAt.After(receivedAt.Add(maxPeerObservationSkew)) {
			return fmt.Errorf("peer observed_at exceeds allowed clock skew of %s", maxPeerObservationSkew)
		}
	}
	wasDown := false
	if previous, previousErr := p.repo.GetInstance(ctx, instanceID); previousErr == nil {
		wasDown = previous.Status == domain.InstanceStatusDown
	} else if !errors.Is(previousErr, gorm.ErrRecordNotFound) {
		return previousErr
	}
	if err := p.repo.UpsertInstance(ctx, &domain.MonitorInstance{
		InstanceID: instanceID, BaseURL: baseURL, Status: domain.InstanceStatusActive, LastSeenAt: &receivedAt, Snapshot: string(raw),
	}); err != nil {
		return err
	}
	if err := p.repo.UpsertSnapshot(ctx, &domain.PeerSnapshot{
		InstanceID: instanceID, BaseURL: baseURL, Status: domain.InstanceStatusActive, Snapshot: string(raw), CheckedAt: observedAt,
	}); err != nil {
		return err
	}
	if wasDown {
		return p.recordAvailabilityTransition(ctx, instanceID, true, receivedAt)
	}
	return nil
}

const peerAlertSpaceID = "moox_system"

func (p *Puller) recordAvailabilityTransition(ctx context.Context, instanceID string, available bool, now time.Time) error {
	if p.alerts == nil {
		return nil
	}
	checkID := "monitor-peer/" + instanceID
	eventType, status, message := domain.AlertEventTriggered, domain.AlertStatusFiring, "monitor peer "+instanceID+" is unavailable"
	state := &domain.AlertState{SpaceID: peerAlertSpaceID, RuleID: checkID, CheckID: checkID, Status: status, FailureCount: 1, OwnerInstanceID: p.ownerInstanceID, TriggeredAt: &now, DedupeKey: checkID}
	if available {
		eventType, status, message = domain.AlertEventResolved, domain.AlertStatusResolved, "monitor peer "+instanceID+" recovered"
		state.Status = status
		state.FailureCount = 0
		state.SuccessCount = 1
		state.ResolvedAt = &now
	}
	digest := sha256.Sum256([]byte(instanceID + "\x00" + eventType + "\x00" + now.Format(time.RFC3339Nano)))
	event := &domain.AlertEvent{
		EventID: "peer-alert-" + hex.EncodeToString(digest[:16]), SpaceID: peerAlertSpaceID,
		RuleID: checkID, CheckID: checkID, EventType: eventType, Status: status,
		OwnerInstanceID: p.ownerInstanceID, Message: message, Payload: "{}", CreatedAt: now,
	}
	return p.alerts.RecordAvailabilityTransition(ctx, state, event)
}
