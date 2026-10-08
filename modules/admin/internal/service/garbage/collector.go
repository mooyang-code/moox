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
	"trpc.group/trpc-go/trpc-go/codec"
	"trpc.group/trpc-go/trpc-go/log"
)

// historyRetention bounds the login and SSH session audit history.
const historyRetention = 90 * 24 * time.Hour

// cloudNodeMgrService 是 CloudNode 管理服务的 tRPC 服务名。
const cloudNodeMgrService = "trpc.moox.cloudnode.CloudNodeMgr"

// CloudNodeGarbage 是 Admin 用到的 gatewayclient 能力：以 admin 身份转发 CloudNodeMgr.CollectGarbage。
// 请求和响应都用 JSON 序列化，Admin 因此不必引用 CloudNode 的协议包。
type CloudNodeGarbage interface {
	Forward(ctx context.Context, servicePath, method string, serialization int, body []byte, opts ...gatewayclient.CallOption) ([]byte, error)
}

// collectGarbageRsp 是 CollectGarbage 响应中用到的字段。tRPC 的 JSON 序列化输出 proto 字段名、枚举数值，
// 64 位整数输出为字符串。
type collectGarbageRsp struct {
	RetInfo *struct {
		Code *int   `json:"code"`
		Msg  string `json:"msg"`
	} `json:"ret_info"`
	Packages     uint32      `json:"packages"`
	CosObjects   uint32      `json:"cos_objects"`
	CosBytes     json.Number `json:"cos_bytes"`
	DeletedNodes uint32      `json:"deleted_nodes"`
	NodeBatches  uint32      `json:"node_batches"`
	Skipped      []string    `json:"skipped"`
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
	raw, err := c.cloudNode.Forward(ctx, cloudNodeMgrService, "CollectGarbage", codec.SerializationTypeJSON, []byte("{}"))
	if err != nil {
		return fmt.Errorf("collect CloudNode garbage: %w", err)
	}
	var rsp collectGarbageRsp
	if err := json.Unmarshal(raw, &rsp); err != nil {
		return fmt.Errorf("collect CloudNode garbage: 解析响应: %w", err)
	}
	if rsp.RetInfo == nil || rsp.RetInfo.Code == nil {
		return errors.New("collect CloudNode garbage: 响应缺少返回码")
	}
	if *rsp.RetInfo.Code != 0 {
		return fmt.Errorf("collect CloudNode garbage: %s", rsp.RetInfo.Msg)
	}
	log.InfoContextf(ctx, "[Garbage] cloudnode packages=%d cos_objects=%d cos_bytes=%s deleted_nodes=%d node_batches=%d skipped=%v",
		rsp.Packages, rsp.CosObjects, rsp.CosBytes, rsp.DeletedNodes, rsp.NodeBatches, rsp.Skipped)
	return nil
}
