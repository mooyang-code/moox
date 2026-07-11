package taskpublisher

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	cloudpb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/modules/collector/internal/domain"
	"google.golang.org/protobuf/encoding/protojson"
)

func TestSubmitMarketOutboxUsesDeterministicOutboxID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req cloudpb.SubmitJobItemsReq
		if err := protojson.Unmarshal(readRequestBody(t, r), &req); err != nil {
			t.Fatal(err)
		}
		item := req.GetItems()[0]
		if item.GetJobItemId() != "outbox-1" || item.GetSpaceId() != "crypto_binance" || item.GetJobType() != "collect.kline" {
			t.Fatalf("item=%+v", item)
		}
		writeProtoJSON(t, w, &cloudpb.SubmitJobItemsRsp{RetInfo: &cloudpb.RetInfo{Code: cloudpb.ErrorCode_SUCCESS}, Acks: []*cloudpb.JobItemAck{{JobItemId: "outbox-1", Status: cloudpb.JobItemAckStatus_JOB_ITEM_ACK_STATUS_DEDUPLICATED}}})
	}))
	defer server.Close()
	client := New(Config{ServiceGatewayTarget: server.URL, Auth: AuthConfig{AccessKey: "ak", SecretKey: "sk"}})
	got, err := client.SubmitMarketOutbox(context.Background(), domain.AttemptOutbox{OutboxID: "outbox-1", ParentJobItemID: "parent", Payload: `{"space_id":"crypto_binance","job_type":"collect.kline","subject_id":"BTC-USDT"}`})
	if err != nil || got != "outbox-1" {
		t.Fatalf("got=%q err=%v", got, err)
	}
}

func TestOutboxPublisherDelaysFailedSubmission(t *testing.T) {
	now := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	repo := &outboxRepoFake{pending: []domain.AttemptOutbox{{OutboxID: "bad", Payload: `{}`}}}
	published, err := (OutboxPublisher{Client: New(Config{}), Repository: repo, Now: func() time.Time { return now }}).PublishPending(context.Background(), 1)
	if err != nil || published != 0 || !repo.delayed.Equal(now.Add(30*time.Second)) {
		t.Fatalf("published=%d delayed=%s err=%v", published, repo.delayed, err)
	}
}

type outboxRepoFake struct {
	pending []domain.AttemptOutbox
	delayed time.Time
}

func (r *outboxRepoFake) ListPendingOutbox(context.Context, time.Time, int) ([]domain.AttemptOutbox, error) {
	return r.pending, nil
}
func (*outboxRepoFake) MarkOutboxPublished(context.Context, string, string, time.Time) error {
	return nil
}
func (r *outboxRepoFake) DelayOutbox(_ context.Context, _ string, next time.Time) error {
	r.delayed = next
	return nil
}
