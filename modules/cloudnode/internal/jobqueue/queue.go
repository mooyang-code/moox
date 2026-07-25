package jobqueue

import (
	"context"
	"time"

	pb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"github.com/mooyang-code/moox/packages/cloudjobqueue"
)

type QueueConfig struct {
	AckWait       time.Duration
	MaxDeliver    int
	MaxAckPending int
}

type ExecutionQueue interface {
	EnsureJobExecutionQueue(ctx context.Context, identity cloudjobqueue.Identity) error
	Publish(ctx context.Context, item *pb.JobItem) error
	Close() error
}
