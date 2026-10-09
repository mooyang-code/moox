package servicecatalog

import (
	"fmt"
	"maps"
	"slices"
)

// VersionHash hashes the directory contents without its version field, matching
// the compiler. It does not mutate the supplied directory.
func (d Directory) VersionHash() (string, error) {
	d.Version = ""
	return contentHash(d)
}

func (d Directory) Validate() error {
	if len(d.Hosts) > 1024 || len(d.Services) > 4096 || d.Hosts == nil || d.Services == nil {
		return fmt.Errorf("directory requires bounded host and service maps")
	}
	for id, host := range d.Hosts {
		if !validDirectoryHost(id, host) {
			return fmt.Errorf("invalid directory host %q", id)
		}
	}
	for path, hosts := range d.Services {
		if !servicePath.MatchString(path) || len(hosts) == 0 || len(hosts) > len(d.Hosts) {
			return fmt.Errorf("invalid directory service %q", path)
		}
		seen := map[string]bool{}
		for _, id := range hosts {
			if _, ok := d.Hosts[id]; !ok || seen[id] {
				return fmt.Errorf("directory service %q has unknown or duplicate host", path)
			}
			seen[id] = true
		}
	}
	hash, err := d.VersionHash()
	if err != nil {
		return err
	}
	if d.Version != hash {
		return fmt.Errorf("directory version does not match its contents")
	}
	return nil
}

func validDirectoryHost(id string, host DirectoryHost) bool {
	return ValidHostID(id) && ValidHostAddress(host.Address) && (host.PrivateAddress == "" || ValidHostAddress(host.PrivateAddress)) && len(host.Region) <= 128
}

// ValidHostID checks the canonical host identity shared by topology and callers.
func ValidHostID(id string) bool { return len(id) <= 128 && identifier.MatchString(id) }

func (d Directory) Clone() Directory {
	out := Directory{Version: d.Version, Hosts: maps.Clone(d.Hosts), Services: maps.Clone(d.Services)}
	for path, hosts := range d.Services {
		out.Services[path] = slices.Clone(hosts)
	}
	return out
}
