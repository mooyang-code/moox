package jobqueue

import (
	"context"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
)

// QueueConfig configures the execution queue adapter.
type QueueConfig struct {
	Naming          NamingConfig
	ExecStream      string
	AckWait         time.Duration
	MaxDeliver      int
	FetchMaxWait    time.Duration
	DefaultMaxBatch int
}

// PublishResult describes the JetStream publish outcome.
type PublishResult struct {
	Created   bool
	Duplicate bool
	Subject   string
	Stream    string
	Sequence  uint64
}

// FetchRequest selects JobItems available to a cloud node.
type FetchRequest struct {
	SpaceID           string
	CodePackageID     string
	SupportedJobTypes []string
	Limit             int
}

// ExecutionQueue is the CloudNode-owned execution queue contract.
type ExecutionQueue interface {
	Publish(ctx context.Context, item *pb.JobItem) (*PublishResult, error)
	Fetch(ctx context.Context, req FetchRequest) ([]Delivery, error)
	Ack(ctx context.Context, ackSubject string) error
	Nak(ctx context.Context, ackSubject string, delay time.Duration) error
	Term(ctx context.Context, ackSubject string) error
	InProgress(ctx context.Context, ackSubject string) error
	Close() error
}
