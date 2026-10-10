package resolver

import (
	"net"
	"net/netip"
)

// IsPublicIP also guards the HTTP dial after DNS resolution. Dialing the
// checked address directly avoids a second DNS lookup and rebinding.
func IsPublicIP(ip net.IP) bool {
	if ip.To4() != nil {
		return isPublicIPv4(ip.To4())
	}
	address, ok := netip.AddrFromSlice(ip)
	if !ok || !netip.MustParsePrefix("2000::/3").Contains(address) {
		return false
	}
	for _, blocked := range []string{"2001::/23", "2001:db8::/32", "2002::/16", "3fff::/20"} {
		if netip.MustParsePrefix(blocked).Contains(address) {
			return false
		}
	}
	return true
}
