package configinit

import (
	"sort"

	"github.com/BurntSushi/toml"
)

// PresetHost 是页面里「使用示例拓扑」按钮预填的一台主机（不含地址和密码）。
type PresetHost struct {
	ID         string   `json:"id"`
	Region     string   `json:"region"`
	Provider   string   `json:"provider"`
	TLSMode    string   `json:"tls_mode"`
	SSHPort    int      `json:"ssh_port"`
	SSHUser    string   `json:"ssh_user"`
	Components []string `json:"components"`
}

// examplePreset 从示例模板里读出三主机拓扑作为预填，避免在代码里再抄一份。
func examplePreset() ([]PresetHost, error) {
	var example struct {
		Hosts map[string]struct {
			Region   string `toml:"region"`
			Provider string `toml:"provider"`
			TLSMode  string `toml:"tls_mode"`
			SSH      struct {
				Port     int    `toml:"port"`
				Username string `toml:"username"`
			} `toml:"ssh"`
		} `toml:"hosts"`
		Placements map[string][]string `toml:"placements"`
	}
	if _, err := toml.Decode(exampleTemplate, &example); err != nil {
		return nil, err
	}
	order := map[string]int{"control": 0, "storage": 1, "compute-1": 2}
	ids := make([]string, 0, len(example.Placements))
	for id := range example.Placements {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		oi, iok := order[ids[i]]
		oj, jok := order[ids[j]]
		switch {
		case iok && jok:
			return oi < oj
		case iok != jok:
			return iok
		default:
			return ids[i] < ids[j]
		}
	})
	preset := make([]PresetHost, 0, len(ids))
	for _, id := range ids {
		host := example.Hosts[id]
		port := host.SSH.Port
		if port == 0 {
			port = 22
		}
		preset = append(preset, PresetHost{
			ID: id, Region: host.Region, Provider: host.Provider, TLSMode: host.TLSMode,
			SSHPort: port, SSHUser: host.SSH.Username, Components: append([]string(nil), example.Placements[id]...),
		})
	}
	return preset, nil
}
