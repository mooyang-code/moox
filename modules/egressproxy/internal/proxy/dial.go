package proxy

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/mooyang-code/moox/modules/egressproxy/internal/resolver"
)

func publicDialer(lookup func(context.Context, string) ([]string, error), dial func(context.Context, string, string) (net.Conn, error)) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || port != "443" {
			return nil, errors.New("egress requires HTTPS port 443")
		}
		lookupCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		addresses, err := lookup(lookupCtx, host)
		cancel()
		if err != nil {
			return nil, errors.New("egress DNS lookup failed")
		}
		attempted := 0
		for _, raw := range addresses {
			ip := net.ParseIP(raw)
			if !resolver.IsPublicIP(ip) {
				continue
			}
			attempted++
			conn, err := dial(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return conn, nil
			}
			if ctx.Err() != nil || attempted >= 16 {
				break
			}
		}
		return nil, errors.New("egress has no reachable public address")
	}
}
