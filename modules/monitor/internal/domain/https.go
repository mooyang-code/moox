package domain

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// HTTPSConfig keeps the public TLS identity separate from the connection
// address. Internal trust requires the CA and its deployment-verified baseline.
type HTTPSConfig struct {
	URL            string `yaml:"url"`
	ConnectAddress string `yaml:"connect_address"`
	ServerName     string `yaml:"server_name"`
	TrustMode      string `yaml:"trust_mode"`
	CAFile         string `yaml:"ca_file"`
	CABaseline     string `yaml:"ca_baseline"`
}

func (c HTTPSConfig) Validate() error {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return fmt.Errorf("HTTPS probe requires an absolute HTTPS URL without user information or fragment")
	}
	if c.ServerName == "" || !strings.EqualFold(c.ServerName, u.Hostname()) {
		return fmt.Errorf("HTTPS probe server_name must match the public URL host")
	}
	host, port, err := net.SplitHostPort(c.ConnectAddress)
	number, portErr := strconv.Atoi(port)
	if err != nil || host == "" || portErr != nil || number < 1 || number > 65535 {
		return fmt.Errorf("HTTPS probe connect_address must be host:port")
	}
	switch c.TrustMode {
	case "internal":
		if c.CAFile == "" || c.CABaseline == "" {
			return fmt.Errorf("internal HTTPS probe requires ca_file and ca_baseline")
		}
	case "public":
		if c.CAFile != "" || c.CABaseline != "" {
			return fmt.Errorf("public HTTPS probe uses system trust and cannot configure an internal CA")
		}
	default:
		return fmt.Errorf("HTTPS probe trust_mode must be internal or public")
	}
	return nil
}
