package command

import (
	"fmt"
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

func metadataSeedSpaceIDs(seed metadataSeed) []string {
	ids := make([]string, 0, len(seed.Spaces))
	for _, item := range seed.Spaces {
		ids = append(ids, item.SpaceID)
	}
	return ids
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
	out := metadataSeed{PrimaryStoreNodes: seed.PrimaryStoreNodes, Devices: seed.Devices}
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
	for _, item := range seed.SubjectSymbols {
		if keep(item.SpaceID) {
			out.SubjectSymbols = append(out.SubjectSymbols, item)
		}
	}
	for _, item := range seed.Datasets {
		if keep(item.SpaceID) {
			out.Datasets = append(out.Datasets, item)
		}
	}
	for _, item := range seed.DatasetSubjects {
		if keep(item.SpaceID) {
			out.DatasetSubjects = append(out.DatasetSubjects, item)
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
	for _, item := range seed.Factors {
		if keep(item.SpaceID) {
			out.Factors = append(out.Factors, item)
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
	for _, item := range seed.ViewColumns {
		if keep(item.SpaceID) {
			out.ViewColumns = append(out.ViewColumns, item)
		}
	}
	for _, item := range seed.PrimaryStoreRoutes {
		if keep(item.SpaceID) {
			out.PrimaryStoreRoutes = append(out.PrimaryStoreRoutes, item)
		}
	}
	return out, nil
}
