package sysdeploy

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"gorm.io/gorm"
)

// EventBusEndpoint 是控制面登记的 EventBus 所在位置。
type EventBusEndpoint struct {
	// HostID 是 EventBus 当前部署的主机。
	HostID string
	// URL 是其他主机访问用的对外地址：tls://<主机地址>:<目录里的端口>。
	URL string
}

// ResolveEventBusEndpoint 按组件部署找出 EventBus 的位置。EventBus 可能不在 Admin 所在的主机上，所以地址跟随部署，
// 并要求恰好一个启用的部署；签发凭据和代为维护都不改动拓扑。
func ResolveEventBusEndpoint(ctx context.Context, db *gorm.DB, controlHostID string) (EventBusEndpoint, error) {
	dao, err := NewTopologyDAO(db, controlHostID)
	if err != nil {
		return EventBusEndpoint{}, err
	}
	hosts, deployments, err := dao.Read(ctx)
	if err != nil {
		return EventBusEndpoint{}, fmt.Errorf("query EventBus deployment: %w", err)
	}
	hostByID := make(map[string]HostRecord, len(hosts))
	for _, host := range hosts {
		hostByID[host.HostID] = host
	}
	var found HostRecord
	for _, deployment := range deployments {
		host, exists := hostByID[deployment.HostID]
		if deployment.ComponentID != "eventbus" || deployment.Status != servicecatalog.Enabled || !exists || host.Status != servicecatalog.Enabled {
			continue
		}
		if found.HostID != "" {
			return EventBusEndpoint{}, errors.New("EventBus must have exactly one active deployment")
		}
		found = host
	}
	if found.HostID == "" {
		return EventBusEndpoint{}, errors.New("active EventBus deployment not found")
	}
	catalog, err := servicecatalog.LoadEmbedded()
	if err != nil {
		return EventBusEndpoint{}, err
	}
	component, ok := catalog.Component("eventbus")
	if !ok || len(component.Ports) != 1 {
		return EventBusEndpoint{}, errors.New("invalid EventBus catalog port")
	}
	raw := "tls://" + net.JoinHostPort(found.Address, strconv.Itoa(component.Ports[0]))
	if _, err := ValidateEventBusNATSURL(raw); err != nil {
		return EventBusEndpoint{}, err
	}
	return EventBusEndpoint{HostID: found.HostID, URL: raw}, nil
}

// ValidateEventBusNATSURL 要求地址只含 tls 协议、主机和端口。
func ValidateEventBusNATSURL(raw string) (*url.URL, error) {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return nil, errors.New("EventBus 的 NATS 地址不能为空，也不能带首尾空白")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "tls" || parsed.Hostname() == "" || parsed.Port() == "" {
		return nil, errors.New("EventBus 的 NATS 地址必须是带主机和端口的 tls 地址")
	}
	if parsed.User != nil || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("EventBus 的 NATS 地址只能包含协议、主机和端口")
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("EventBus 的 NATS 地址端口必须在 1 到 65535 之间")
	}
	return parsed, nil
}
