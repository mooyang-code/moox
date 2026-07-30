package command

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	setupclient "github.com/mooyang-code/moox/modules/cli/internal/setup/client"
)

type metadataSpaceChoice struct {
	SpaceID     string `json:"space_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

func metadataSpaceCatalog(seed metadataSeed) []metadataSpaceChoice {
	choices := make([]metadataSpaceChoice, 0, len(seed.Spaces))
	for _, item := range seed.Spaces {
		if item.Attributes["scope"] == "internal" {
			continue
		}
		choices = append(choices, metadataSpaceChoice{SpaceID: item.SpaceID, Name: item.Name, Description: item.Description})
	}
	return choices
}

func businessSetupSpaces(seed metadataSeed) ([]setupclient.Space, error) {
	spaces := make([]setupclient.Space, 0, len(seed.Spaces))
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
		if item.Attributes["scope"] == "internal" {
			continue
		}
		if strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.Market) == "" || strings.TrimSpace(item.Timezone) == "" {
			return nil, fmt.Errorf("metadata space %q requires name, market, and timezone", spaceID)
		}
		attributes := item.Attributes
		if attributes == nil {
			attributes = map[string]string{}
		}
		attributesJSON, err := json.Marshal(attributes)
		if err != nil {
			return nil, fmt.Errorf("encode metadata space %q attributes: %w", spaceID, err)
		}
		spaces = append(spaces, setupclient.Space{
			SpaceID: spaceID, Name: strings.TrimSpace(item.Name),
			Description: strings.TrimSpace(item.Description), Owner: strings.TrimSpace(item.Owner),
			Market: strings.TrimSpace(item.Market), Timezone: strings.TrimSpace(item.Timezone),
			Status: item.status(), AttributesJSON: string(attributesJSON),
		})
	}
	sort.Slice(spaces, func(i, j int) bool { return spaces[i].SpaceID < spaces[j].SpaceID })
	return spaces, nil
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
	out := metadataSeed{Devices: seed.Devices}
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
	return out, nil
}
