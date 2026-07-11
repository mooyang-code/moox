package taskpublisher

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	cloudpb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"github.com/mooyang-code/moox/modules/collector/internal/repository"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/types/known/structpb"
)

type OutboxRepository interface {
	ListPendingOutbox(context.Context, time.Time, int) ([]domain.AttemptOutbox, error)
	MarkOutboxPublished(context.Context, string, string, time.Time) error
	DelayOutbox(context.Context, string, time.Time) error
}
type OutboxPublisher struct {
	Client     *Client
	Repository OutboxRepository
	Preparer   interface {
		Prepare(context.Context, domain.AttemptOutbox, time.Time) (domain.AttemptOutbox, error)
	}
	Guard interface{ RequireLeader(context.Context) error }
	Now   func() time.Time
}

func (p OutboxPublisher) PublishPending(ctx context.Context, limit int) (int, error) {
	if p.Client == nil || p.Repository == nil {
		return 0, fmt.Errorf("outbox client and repository are required")
	}
	if p.Guard != nil {
		if err := p.Guard.RequireLeader(ctx); err != nil {
			return 0, err
		}
	}
	now := time.Now().UTC()
	if p.Now != nil {
		now = p.Now().UTC()
	}
	values, err := p.Repository.ListPendingOutbox(ctx, now, limit)
	if err != nil {
		return 0, err
	}
	published := 0
	for _, value := range values {
		if p.Preparer != nil {
			value, err = p.Preparer.Prepare(ctx, value, now)
			if err != nil {
				_ = p.Repository.DelayOutbox(ctx, value.OutboxID, now.Add(30*time.Second))
				continue
			}
		}
		jobItemID, err := p.Client.SubmitMarketOutbox(ctx, value)
		if err != nil {
			_ = p.Repository.DelayOutbox(ctx, value.OutboxID, now.Add(30*time.Second))
			continue
		}
		if err := p.Repository.MarkOutboxPublished(ctx, value.OutboxID, jobItemID, now); err != nil {
			return published, err
		}
		published++
	}
	return published, nil
}

func (c *Client) SubmitMarketOutbox(ctx context.Context, value domain.AttemptOutbox) (string, error) {
	params := map[string]any{}
	if err := json.Unmarshal([]byte(value.Payload), &params); err != nil {
		return "", err
	}
	spaceID := valueString(params, "space_id", "")
	jobType := valueString(params, "job_type", "")
	if spaceID == "" || jobType == "" || strings.TrimSpace(value.OutboxID) == "" {
		return "", fmt.Errorf("outbox space_id, job_type and id are required")
	}
	payload, err := structpb.NewStruct(params)
	if err != nil {
		return "", err
	}
	item := &cloudpb.JobItem{SpaceId: spaceID, JobId: value.ParentJobItemID, JobItemId: value.OutboxID, JobType: jobType, CodePackageId: valueString(params, "code_package_id", defaultCodePackageID(params)), Params: payload, Priority: 100}
	raw, err := protojson.Marshal(&cloudpb.SubmitJobItemsReq{Items: []*cloudpb.JobItem{item}})
	if err != nil {
		return "", err
	}
	var rsp cloudpb.SubmitJobItemsRsp
	if err := c.postService(ctx, "cloudnode", "SubmitJobItems", raw, &rsp); err != nil {
		return "", err
	}
	if rsp.GetRetInfo().GetCode() != cloudpb.ErrorCode_SUCCESS {
		return "", fmt.Errorf("submit outbox: %s", rsp.GetRetInfo().GetMsg())
	}
	if len(rsp.GetAcks()) != 1 {
		return "", fmt.Errorf("submit outbox returned %d acks", len(rsp.GetAcks()))
	}
	ack := rsp.GetAcks()[0]
	if ack.GetStatus() != cloudpb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_CREATED && ack.GetStatus() != cloudpb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED {
		return "", fmt.Errorf("submit outbox was not accepted")
	}
	return ack.GetJobItemId(), nil
}

var _ OutboxRepository = (*repository.MarketAttemptRepository)(nil)

var defaultOutbox struct {
	sync.RWMutex
	publisher *OutboxPublisher
}

func SetDefaultOutboxPublisher(value *OutboxPublisher) {
	defaultOutbox.Lock()
	defaultOutbox.publisher = value
	defaultOutbox.Unlock()
}
func HandleOutbox(ctx context.Context, _ string) error {
	defaultOutbox.RLock()
	publisher := defaultOutbox.publisher
	defaultOutbox.RUnlock()
	if publisher == nil {
		return nil
	}
	_, err := publisher.PublishPending(ctx, 100)
	return err
}
