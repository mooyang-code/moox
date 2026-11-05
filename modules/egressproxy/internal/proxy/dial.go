package proxy

import (
	"context"
	"errors"
	"net"

	"github.com/mooyang-code/moox/packages/netguard"
)

// publicDialer 只拨号到 443 端口上的公网地址：地址检查与直接用 IP 拨号由 netguard 负责。
func publicDialer(lookup func(context.Context, string) ([]string, error), dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	guarded := netguard.NewDialContext(netguard.DialOptions{Lookup: lookup, Dial: dial, AllowedPorts: []string{"443"}})
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := guarded(ctx, network, address)
		switch {
		case errors.Is(err, netguard.ErrPortNotAllowed):
			return nil, errors.New("egress requires HTTPS port 443")
		case errors.Is(err, netguard.ErrLookupFailed):
			return nil, errors.New("egress DNS lookup failed")
		case err != nil:
			return nil, errors.New("egress has no reachable public address")
		}
		return conn, nil
	}
}
