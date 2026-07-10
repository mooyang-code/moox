//go:build !linux

package collector

import (
	"context"
	"fmt"
	"github.com/mooyang-code/moox/packages/hostmetricpb"
)

type Collector struct{}

func New() *Collector { return &Collector{} }
func (c *Collector) Collect(context.Context) (*hostmetricpb.HostSnapshot, []*hostmetricpb.CollectorStatus, error) {
	return nil, nil, fmt.Errorf("host agent supports linux only")
}
