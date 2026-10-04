package eventpublisher

import (
	"context"
	"time"

	"github.com/mooyang-code/moox/packages/hostmetricpb"
)

type Publisher interface {
	PublishHostMetric(context.Context, string, *hostmetricpb.HostMetric, time.Time) error
	Ready() bool
	Close() error
}
