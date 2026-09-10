package config

import (
	"fmt"
	"strings"
)

func (cfg SCFFetcherSpace) IsRegionBlacklisted(region string) bool {
	for _, blocked := range cfg.RegionBlacklist {
		if strings.EqualFold(strings.TrimSpace(blocked), strings.TrimSpace(region)) {
			return true
		}
	}
	return false
}

func normalizeSCFRegionBlacklist(cfg *SCFFetcherSpace, path string) error {
	regions := make([]string, 0, len(cfg.RegionBlacklist))
	seen := make(map[string]bool, len(cfg.RegionBlacklist))
	for _, value := range cfg.RegionBlacklist {
		region := strings.ToLower(strings.TrimSpace(value))
		if !supportedSCFRegion(region) {
			return fmt.Errorf("config_invalid: %s.region_blacklist contains unsupported region %q", path, value)
		}
		if !seen[region] {
			regions = append(regions, region)
			seen[region] = true
		}
	}
	cfg.RegionBlacklist = regions
	if cfg.IsRegionBlacklisted(cfg.InstrumentSnapshotRegion) {
		return fmt.Errorf("config_invalid: %s.instrument_snapshot_region %q is blacklisted", path, cfg.InstrumentSnapshotRegion)
	}
	return nil
}
