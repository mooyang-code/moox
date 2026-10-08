// Package garbage runs the daily system garbage collection. Admin trims its
// own history tables and asks each module that owns cloud resources to drop
// what nothing uses any more; modules with their own retention (Collector,
// Monitor, Storage Views) are not involved.
package garbage

import (
	"context"
	"errors"
	"fmt"
	"time"

	cloudnodepb "github.com/mooyang-code/moox/modules/cloudnode/proto/cloudnodegen"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/client"
	"trpc.group/trpc-go/trpc-go/log"
)

// historyRetention bounds the login and SSH session audit history.
const historyRetention = 90 * 24 * time.Hour

// CloudNodeGarbage 是 CloudNode 的垃圾回收接口；Admin 以 admin 身份经 gatewayclient 调用。
type CloudNodeGarbage interface {
	CollectGarbage(context.Context, *cloudnodepb.CollectGarbageReq, ...client.Option) (*cloudnodepb.CollectGarbageRsp, error)
}

// Collector removes system garbage once per timer invocation.
type Collector struct {
	db        *gorm.DB
	cloudNode CloudNodeGarbage
	now       func() time.Time
}

// NewCollector returns a Collector over the Admin database. cloudNode 为空时（没有配置 admin 身份的
// gateway_client）只清理 Admin 自己的历史，并在每次运行时报告 CloudNode 未清理。
func NewCollector(db *gorm.DB, cloudNode CloudNodeGarbage) (*Collector, error) {
	if db == nil {
		return nil, errors.New("garbage collector requires the admin database")
	}
	return &Collector{db: db, cloudNode: cloudNode, now: time.Now}, nil
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
	if c.cloudNode == nil {
		return errors.New("collect CloudNode garbage: admin 身份的 gateway_client 没有配置")
	}
	rsp, err := c.cloudNode.CollectGarbage(ctx, &cloudnodepb.CollectGarbageReq{})
	if err != nil {
		return fmt.Errorf("collect CloudNode garbage: %w", err)
	}
	if rsp.GetRetInfo().GetCode() != cloudnodepb.ErrorCode_SUCCESS {
		return fmt.Errorf("collect CloudNode garbage: %s", rsp.GetRetInfo().GetMsg())
	}
	log.InfoContextf(ctx, "[Garbage] cloudnode packages=%d cos_objects=%d cos_bytes=%d deleted_nodes=%d",
		rsp.GetPackages(), rsp.GetCosObjects(), rsp.GetCosBytes(), rsp.GetDeletedNodes())
	return nil
}
