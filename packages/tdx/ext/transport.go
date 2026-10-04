// Package ext exposes the extended TDX transport as a separate composition
// root. Extended classic and MAC sessions must not accidentally inherit the
// normal 7709 setup handshake.
package ext

import (
	"time"

	tdx "github.com/mooyang-code/moox/packages/tdx"
)

type ProtocolVariant = tdx.ProtocolVariant
type Client = tdx.ExtendedClient

const (
	ProtocolClassic = tdx.ProtocolExClassic
	ProtocolMAC     = tdx.ProtocolExMAC
)

func NewClient(host string, port int, variant ProtocolVariant, timeout time.Duration) (*Client, error) {
	return tdx.NewExtendedClient(host, port, variant, timeout)
}
