package taskpublisher

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
)

type LeaseWriter interface {
	PutLease(context.Context, repository.MarketLease) error
}

type OutboxLeasePreparer struct {
	Leases LeaseWriter
	TTL    time.Duration
}

func (p OutboxLeasePreparer) Prepare(ctx context.Context, value domain.AttemptOutbox, now time.Time) (domain.AttemptOutbox, error) {
	if p.Leases == nil {
		return value, fmt.Errorf("lease writer is required")
	}
	params := map[string]any{}
	if err := json.Unmarshal([]byte(value.Payload), &params); err != nil {
		return value, err
	}
	if valueString(params, "job_type", "") != "collect.kline" {
		return value, nil
	}
	marketID, providerID := valueString(params, "market_id", ""), valueString(params, "provider_id", "")
	unifiedDatasetID, subjectID := valueString(params, "unified_dataset_id", ""), valueString(params, "subject_id", "")
	frequency, start, end := valueString(params, "frequency", ""), valueString(params, "start_time", ""), valueString(params, "end_time", "")
	if marketID == "" || providerID == "" || unifiedDatasetID == "" || subjectID == "" || frequency == "" || start == "" || end == "" {
		return value, fmt.Errorf("market outbox is missing lease scope")
	}
	ttl := p.TTL
	if ttl <= 0 {
		ttl = 2 * time.Minute
	}
	epoch := now.UTC().UnixMicro()
	fixedWindow := start + "/" + end
	providerKey := strings.Join([]string{marketID, providerID, fixedWindow}, "|")
	resolutionKey := strings.Join([]string{marketID, unifiedDatasetID, subjectID, frequency, fixedWindow}, "|")
	providerLeaseID, resolutionLeaseID := outboxLeaseID("provider", providerKey), outboxLeaseID("resolution", resolutionKey)
	for _, lease := range []repository.MarketLease{
		{LeaseID: providerLeaseID, LeaseType: "provider", LeaseKey: providerKey, Epoch: epoch, OwnerID: value.OutboxID, ExpiresAt: now.UTC().Add(ttl)},
		{LeaseID: resolutionLeaseID, LeaseType: "resolution", LeaseKey: resolutionKey, Epoch: epoch, OwnerID: value.OutboxID, ExpiresAt: now.UTC().Add(ttl)},
	} {
		if err := p.Leases.PutLease(ctx, lease); err != nil {
			return value, err
		}
	}
	params["quota_lease_id"] = providerLeaseID
	params["lease_epoch"] = strconv.FormatInt(epoch, 10)
	params["resolution_lease_id"] = resolutionLeaseID
	params["resolution_lease_epoch"] = strconv.FormatInt(epoch, 10)
	params["execution_nonce"] = value.OutboxID
	raw, err := json.Marshal(params)
	if err != nil {
		return value, err
	}
	value.Payload = string(raw)
	return value, nil
}

func outboxLeaseID(kind, key string) string {
	sum := sha256.Sum256([]byte(kind + "|" + key))
	return hex.EncodeToString(sum[:])
}
