package command

import (
	"fmt"
	"sort"
	"strings"
)

type metadataSpaceChoice struct {
	SpaceID     string `json:"space_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func metadataSpaceCatalog(seed metadataSeed) []metadataSpaceChoice {
	choices := make([]metadataSpaceChoice, 0, len(seed.Spaces))
	for _, item := range seed.Spaces {
		choices = append(choices, metadataSpaceChoice{SpaceID: item.SpaceID, Name: item.Name, Description: item.Description})
	}
	return choices
}

// businessSpaceIDs 校验种子里的业务空间（名称、市场、时区必填，ID 不重复），按 ID 排序返回。
// 空间只登记在 Storage：随元数据经 metadata apply 写入。
func businessSpaceIDs(seed metadataSeed) ([]string, error) {
	ids := make([]string, 0, len(seed.Spaces))
	seen := make(map[string]struct{}, len(seed.Spaces))
	for _, item := range seed.Spaces {
		spaceID := strings.TrimSpace(item.SpaceID)
		if spaceID == "" {
			return nil, fmt.Errorf("metadata space_id is required")
		}
		if _, ok := seen[spaceID]; ok {
			return nil, fmt.Errorf("duplicate metadata space %q", spaceID)
		}
		seen[spaceID] = struct{}{}
		if strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.Market) == "" || strings.TrimSpace(item.Timezone) == "" {
			return nil, fmt.Errorf("metadata space %q requires name, market, and timezone", spaceID)
		}
		ids = append(ids, spaceID)
	}
	sort.Strings(ids)
	return ids, nil
}

func selectMetadataSpaces(seed metadataSeed, requested []string) (metadataSeed, error) {
	if len(requested) == 0 {
		return seed, nil
	}
	aliases := make(map[string]string, len(seed.Spaces)*2)
	for _, item := range seed.Spaces {
		aliases[strings.ToLower(strings.TrimSpace(item.SpaceID))] = item.SpaceID
		if name := strings.TrimSpace(item.Name); name != "" {
			aliases[strings.ToLower(name)] = item.SpaceID
		}
	}
	selected := make(map[string]struct{}, len(requested))
	for _, raw := range requested {
		value := strings.TrimSpace(raw)
		spaceID, ok := aliases[strings.ToLower(value)]
		if !ok {
			return metadataSeed{}, fmt.Errorf("unknown metadata space %q", value)
		}
		if _, exists := selected[spaceID]; exists {
			return metadataSeed{}, fmt.Errorf("duplicate metadata space %q", spaceID)
		}
		selected[spaceID] = struct{}{}
	}
	keep := func(spaceID string) bool { _, ok := selected[spaceID]; return ok }
	out := metadataSeed{}
	for _, item := range seed.Spaces {
		if keep(item.SpaceID) {
			out.Spaces = append(out.Spaces, item)
		}
	}
	for _, item := range seed.DataSources {
		if keep(item.SpaceID) {
			out.DataSources = append(out.DataSources, item)
		}
	}
	for _, item := range seed.Subjects {
		if keep(item.SpaceID) {
			out.Subjects = append(out.Subjects, item)
		}
	}
	for _, item := range seed.Tags {
		if keep(item.SpaceID) {
			out.Tags = append(out.Tags, item)
		}
	}
	for _, item := range seed.Datasets {
		if keep(item.SpaceID) {
			out.Datasets = append(out.Datasets, item)
		}
	}
	for _, item := range seed.FieldGroups {
		if keep(item.SpaceID) {
			out.FieldGroups = append(out.FieldGroups, item)
		}
	}
	for _, item := range seed.Fields {
		if keep(item.SpaceID) {
			out.Fields = append(out.Fields, item)
		}
	}
	for _, item := range seed.DatasetColumns {
		if keep(item.SpaceID) {
			out.DatasetColumns = append(out.DatasetColumns, item)
		}
	}
	for _, item := range seed.Views {
		if keep(item.SpaceID) {
			out.Views = append(out.Views, item)
		}
	}
	return out, nil
}
