// Package garbage runs the daily system garbage collection. Admin trims its
// own history tables and asks each module that owns cloud resources to drop
// what nothing uses any more; modules with their own retention (Collector,
// Monitor, Storage Views) are not involved.
package garbage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/mooyang-code/moox/packages/gatewayclient"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

// historyRetention bounds the login and SSH session audit history.
const historyRetention = 90 * 24 * time.Hour

// Collector borrows the process gateway signed as admin.
type Collector struct {
	db      *gorm.DB
	gateway gatewayclient.Invoker
	now     func() time.Time
}

func NewCollector(db *gorm.DB, gateway gatewayclient.Invoker) (*Collector, error) {
	if db == nil || gateway == nil {
		return nil, errors.New("garbage collector requires database and admin gateway client")
	}
	return &Collector{db: db, gateway: gateway, now: time.Now}, nil
}

// Run trims Admin history and collects CloudNode garbage. One failing part
// does not stop the other; their errors are joined.
func (c *Collector) Run(ctx context.Context) error {
	return errors.Join(c.trimHistory(ctx), c.collectCloudNode(ctx))
}

func (c *Collector) trimHistory(ctx context.Context) error {
	cutoff := c.now().UTC().Add(-historyRetention).Format("2006-01-02 15:04:05")
	logins := c.db.WithContext(ctx).Exec(`DELETE FROM t_login_history WHERE c_ctime < ?`, cutoff)
	if logins.Error != nil {
		return fmt.Errorf("trim login history: %w", logins.Error)
	}
	// A session that has been "connected" for longer than the retention is a
	// leftover of a crashed Admin, so the connect time bounds it too.
	sessions := c.db.WithContext(ctx).Exec(`DELETE FROM t_ssh_session WHERE COALESCE(c_close_time, c_connect_time) < ?`, cutoff)
	if sessions.Error != nil {
		return fmt.Errorf("trim SSH session history: %w", sessions.Error)
	}
	log.InfoContextf(ctx, "[Garbage] trimmed admin history login_history=%d ssh_sessions=%d", logins.RowsAffected, sessions.RowsAffected)
	return nil
}

func (c *Collector) collectCloudNode(ctx context.Context) error {
	var summary cloudNodeGarbageSummary
	if err := c.gateway.Invoke(ctx, "trpc.moox.cloudnode.CloudNodeMgr", "CollectGarbage", struct {
		DryRun bool `json:"dry_run"`
	}{}, &summary); err != nil {
		return fmt.Errorf("collect CloudNode garbage: %w", err)
	}
	if summary.RetInfo.Code != 0 {
		return fmt.Errorf("collect CloudNode garbage: %s", summary.RetInfo.Msg)
	}
	log.InfoContextf(ctx, "[Garbage] cloudnode packages=%d cos_objects=%d cos_bytes=%s deleted_nodes=%d node_batches=%d skipped=%v", summary.Packages, summary.COSObjects, summary.COSBytes, summary.DeletedNodes, summary.NodeBatches, summary.Skipped)
	return nil
}

// Admin keeps business modules behind the generic native gateway boundary.
type cloudNodeGarbageSummary struct {
	RetInfo struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	} `json:"ret_info"`
	Packages     uint32      `json:"packages"`
	COSObjects   uint32      `json:"cos_objects"`
	COSBytes     json.Number `json:"cos_bytes"`
	DeletedNodes uint32      `json:"deleted_nodes"`
	NodeBatches  uint32      `json:"node_batches"`
	Skipped      []string    `json:"skipped"`
}
