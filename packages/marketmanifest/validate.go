package marketmanifest

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

func (m Manifest) Validate(directory string) error {
	if m.SchemaVersion != 1 {
		return fmt.Errorf("unsupported schema_version %d", m.SchemaVersion)
	}
	if m.MarketID == "" || m.SpaceID == "" {
		return fmt.Errorf("market_id and space_id are required")
	}
	if directory != "" && directory != m.MarketID {
		return fmt.Errorf("directory %q does not match market_id %q", directory, m.MarketID)
	}
	if m.SpaceID != m.MarketID {
		return fmt.Errorf("space_id %q must match market_id %q", m.SpaceID, m.MarketID)
	}
	if m.Execution.JobBudgetMS < 0 || m.Execution.JobBudgetMS > 30000 {
		return fmt.Errorf("job_budget_ms must be between 0 and 30000")
	}
	if m.Execution.ReportReserveMS < 0 {
		return fmt.Errorf("report_reserve_ms must not be negative")
	}
	if m.Execution.TimeoutSeconds <= 0 || m.Execution.TimeoutSeconds*1000 < m.Execution.JobBudgetMS+m.Execution.ReportReserveMS {
		return fmt.Errorf("timeout_seconds must cover job budget and report reserve")
	}
	providers := make(map[string]struct{}, len(m.Providers))
	for _, provider := range m.Providers {
		if provider.ID == "" {
			return fmt.Errorf("provider id is required")
		}
		if _, exists := providers[provider.ID]; exists {
			return fmt.Errorf("duplicate provider id %q", provider.ID)
		}
		providers[provider.ID] = struct{}{}
		for _, quota := range provider.Quotas {
			if quota.Scope == "" || quota.WindowSeconds <= 0 || quota.Limit < 0 || quota.Weight <= 0 {
				return fmt.Errorf("invalid quota for provider %q", provider.ID)
			}
		}
	}
	datasets := make(map[string]struct{}, len(m.Datasets))
	for _, dataset := range m.Datasets {
		if dataset.ID == "" {
			return fmt.Errorf("dataset id is required")
		}
		if _, exists := datasets[dataset.ID]; exists {
			return fmt.Errorf("duplicate dataset id %q", dataset.ID)
		}
		datasets[dataset.ID] = struct{}{}
		if dataset.ProviderID != "" {
			if _, exists := providers[dataset.ProviderID]; !exists {
				return fmt.Errorf("dataset %q references unknown provider %q", dataset.ID, dataset.ProviderID)
			}
		}
	}
	for _, feed := range m.Feeds {
		if feed.ID == "" || feed.DatasetID == "" {
			return fmt.Errorf("feed id and dataset_id are required")
		}
		if _, exists := datasets[feed.DatasetID]; !exists {
			return fmt.Errorf("feed %q references unknown dataset %q", feed.ID, feed.DatasetID)
		}
	}
	return nil
}

func rejectSecrets(data []byte) error {
	var node yaml.Node
	if err := yaml.Unmarshal(data, &node); err != nil {
		return err
	}
	var walk func(*yaml.Node) error
	walk = func(current *yaml.Node) error {
		if current.Kind == yaml.MappingNode {
			for i := 0; i+1 < len(current.Content); i += 2 {
				key, value := current.Content[i], current.Content[i+1]
				name := strings.ToLower(key.Value)
				if name != "credential_env" && (name == "token" || name == "secret" || name == "password" || name == "api_key" || name == "cookie" || strings.Contains(name, "secret")) {
					return fmt.Errorf("embedded secret-like field %q is forbidden", key.Value)
				}
				if err := walk(value); err != nil {
					return err
				}
			}
		}
		for _, child := range current.Content {
			if err := walk(child); err != nil {
				return err
			}
		}
		return nil
	}
	return walk(&node)
}
