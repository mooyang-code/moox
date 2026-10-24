// Package garbage runs the daily system garbage collection. Admin asks each
// module that owns cloud resources to drop what nothing uses any more;
// modules with their own retention (Collector, Monitor, Storage Views) are not
// involved.
package garbage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"trpc.group/trpc-go/trpc-go/log"
)

// Collector borrows the process gateway signed as admin.
type Collector struct {
	gateway gatewayclient.Invoker
}

func NewCollector(gateway gatewayclient.Invoker) (*Collector, error) {
	if gateway == nil {
		return nil, errors.New("garbage collector requires admin gateway client")
	}
	return &Collector{gateway: gateway}, nil
}

// Run collects FunctionInstance garbage.
func (c *Collector) Run(ctx context.Context) error {
	return c.collectCloudFunction(ctx)
}

func (c *Collector) collectCloudFunction(ctx context.Context) error {
	var summary cloudFunctionGarbageSummary
	if err := c.gateway.Invoke(ctx, "trpc.moox.cloudfunction.CloudFunctionMgr", "CollectGarbage", struct {
		DryRun bool `json:"dry_run"`
	}{}, &summary); err != nil {
		return fmt.Errorf("collect FunctionInstance garbage: %w", err)
	}
	if summary.RetInfo.Code != 0 {
		return fmt.Errorf("collect FunctionInstance garbage: %s", summary.RetInfo.Msg)
	}
	log.InfoContextf(ctx, "[Garbage] cloudfunction packages=%d cos_objects=%d cos_bytes=%s deleted_instances=%d function_jobs=%d skipped=%v", summary.Packages, summary.COSObjects, summary.COSBytes, summary.DeletedInstances, summary.FunctionJobs, summary.Skipped)
	return nil
}

// Admin keeps business modules behind the generic native gateway boundary.
type cloudFunctionGarbageSummary struct {
	RetInfo struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	} `json:"ret_info"`
	Packages         uint32      `json:"packages"`
	COSObjects       uint32      `json:"cos_objects"`
	COSBytes         json.Number `json:"cos_bytes"`
	DeletedInstances uint32      `json:"deleted_instances"`
	FunctionJobs     uint32      `json:"function_jobs"`
	Skipped          []string    `json:"skipped"`
}
