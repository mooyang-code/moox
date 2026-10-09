// Package garbage runs the daily system garbage collection. Admin trims its
// own history tables and asks each module that owns cloud resources to drop
// what nothing uses any more; modules with their own retention (Collector,
// Monitor, Storage Views) are not involved.
package garbage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/mooyang-code/moox/modules/admin/internal/console"
	"gorm.io/gorm"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// historyRetention bounds the login and SSH session audit history.
	historyRetention = 90 * 24 * time.Hour
	// maxResponseBytes bounds a module's garbage summary.
	maxResponseBytes = 1 << 20
)

// ServiceResolver finds a module deployment on the Admin node.
type ServiceResolver interface {
	ResolveAdminServiceDetail(ctx context.Context, adminNodeID, serviceID string) (console.ServiceDetail, bool)
}

// Collector removes system garbage once per timer invocation.
type Collector struct {
	db          *gorm.DB
	resolver    ServiceResolver
	adminNodeID string
	client      *http.Client
	now         func() time.Time
}

// NewCollector returns a Collector over the Admin database and deployments.
func NewCollector(db *gorm.DB, resolver ServiceResolver, adminNodeID string) (*Collector, error) {
	if db == nil || resolver == nil || adminNodeID == "" {
		return nil, errors.New("garbage collector requires database, service resolver and admin node id")
	}
	return &Collector{db: db, resolver: resolver, adminNodeID: adminNodeID, client: &http.Client{}, now: time.Now}, nil
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

type retInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (c *Collector) collectCloudNode(ctx context.Context) error {
	detail, ok := c.resolver.ResolveAdminServiceDetail(ctx, c.adminNodeID, "cloudnode")
	if !ok {
		return errors.New("collect CloudNode garbage: cloudnode has no active deployment on the admin node")
	}
	url := fmt.Sprintf("http://%s/%s/CollectGarbage", detail.Address, detail.Path)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader([]byte(`{}`)))
	if err != nil {
		return fmt.Errorf("collect CloudNode garbage: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return fmt.Errorf("collect CloudNode garbage: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return fmt.Errorf("collect CloudNode garbage: read response: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("collect CloudNode garbage: HTTP %d", response.StatusCode)
	}
	var summary struct {
		RetInfo retInfo `json:"ret_info"`
	}
	if err := json.Unmarshal(body, &summary); err != nil {
		return fmt.Errorf("collect CloudNode garbage: decode response: %w", err)
	}
	if summary.RetInfo.Code != 0 {
		return fmt.Errorf("collect CloudNode garbage: %s", summary.RetInfo.Msg)
	}
	log.InfoContextf(ctx, "[Garbage] cloudnode %s", body)
	return nil
}
