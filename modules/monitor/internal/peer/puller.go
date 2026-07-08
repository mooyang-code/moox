package peer

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/monitor/internal/domain"
	"github.com/mooyang-code/moox/modules/monitor/internal/repository"
)

type Remote struct {
	InstanceID string
	BaseURL    string
	Token      string
}

type PullerOptions struct {
	Peers   []Remote
	Timeout time.Duration
	Client  *http.Client
}

type Puller struct {
	repo    *repository.PeerRepository
	peers   []Remote
	timeout time.Duration
	client  *http.Client
}

func NewPuller(repo *repository.PeerRepository, opts PullerOptions) *Puller {
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := opts.Client
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}
	return &Puller{repo: repo, peers: opts.Peers, timeout: timeout, client: client}
}

func (p *Puller) PullOnce(ctx context.Context) error {
	for _, remote := range p.peers {
		if err := p.pullRemote(ctx, remote); err != nil {
			return err
		}
	}
	return nil
}

func (p *Puller) MarkStale(ctx context.Context, now time.Time, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = p.timeout
	}
	return p.repo.MarkStale(ctx, now.Add(-3*timeout))
}

func (p *Puller) pullRemote(ctx context.Context, remote Remote) error {
	url := strings.TrimRight(remote.BaseURL, "/") + "/internal/monitor/v1/snapshot"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	if remote.Token != "" {
		req.Header.Set(PeerTokenHeader, remote.Token)
	}
	rsp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer rsp.Body.Close()
	if rsp.StatusCode >= 400 {
		return fmt.Errorf("peer %s returned HTTP %d", remote.InstanceID, rsp.StatusCode)
	}
	var snapshot Snapshot
	if err := json.NewDecoder(rsp.Body).Decode(&snapshot); err != nil {
		return err
	}
	if snapshot.InstanceID == "" {
		snapshot.InstanceID = remote.InstanceID
	}
	if snapshot.BaseURL == "" {
		snapshot.BaseURL = remote.BaseURL
	}
	raw, _ := json.Marshal(snapshot)
	seenAt := snapshot.ObservedAt
	if seenAt.IsZero() {
		seenAt = time.Now().UTC()
	}
	if err := p.repo.UpsertInstance(ctx, &domain.MonitorInstance{
		InstanceID: snapshot.InstanceID,
		BaseURL:    snapshot.BaseURL,
		Status:     domain.InstanceStatusActive,
		LastSeenAt: &seenAt,
		Snapshot:   string(raw),
	}); err != nil {
		return err
	}
	return p.repo.UpsertSnapshot(ctx, &domain.PeerSnapshot{
		InstanceID: snapshot.InstanceID,
		BaseURL:    snapshot.BaseURL,
		Status:     domain.InstanceStatusActive,
		Snapshot:   string(raw),
		CheckedAt:  seenAt,
	})
}
