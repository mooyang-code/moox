package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/mooyang-code/moox/packages/marketmanifest"
	"github.com/spf13/cobra"
)

var (
	marketInitSelection   string
	marketInitMetadataURL string
)

var builtinMarketIDs = []string{"stock_cn", "stock_us", "crypto_binance", "crypto_okx"}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize built-in platform resources",
}

var initMarketsCmd = &cobra.Command{
	Use:   "markets",
	Short: "Register built-in market spaces and their dataset contracts",
	RunE: func(cmd *cobra.Command, _ []string) error {
		root := builtinMarketConfigRoot()
		summary, err := runBuiltinMarketInit(cmd.Context(), root, marketInitSelection, func(ctx context.Context, marketID string, calls []metadataImportCall) (metadataImportSummary, error) {
			return runMetadataApply(ctx, defaultMetadataImportURL(marketInitMetadataURL), calls)
		})
		if err != nil {
			return err
		}
		return json.NewEncoder(cmd.OutOrStdout()).Encode(summary)
	},
}

type builtinMarketInitSummary struct {
	Markets []string `json:"markets"`
	Total   struct {
		Applied int `json:"applied"`
		Failed  int `json:"failed"`
	} `json:"total"`
	Results map[string]metadataImportSummary `json:"results"`
}
type marketInitApply func(context.Context, string, []metadataImportCall) (metadataImportSummary, error)

func runBuiltinMarketInit(ctx context.Context, root, selection string, apply marketInitApply) (builtinMarketInitSummary, error) {
	ids, err := parseBuiltinMarketIDs(selection)
	if err != nil {
		return builtinMarketInitSummary{}, err
	}
	if apply == nil {
		return builtinMarketInitSummary{}, fmt.Errorf("market metadata apply function is required")
	}
	summary := builtinMarketInitSummary{Markets: ids, Results: make(map[string]metadataImportSummary, len(ids))}
	for _, id := range ids {
		manifestPath := filepath.Join(root, id, "market.yaml")
		manifest, err := marketmanifest.LoadFile(manifestPath)
		if err != nil {
			return summary, err
		}
		if !manifest.RegisterMetadata {
			continue
		}
		seed, err := loadMetadataSeed(filepath.Join(root, id, "metadata.seed.yaml"))
		if err != nil {
			return summary, err
		}
		calls, err := buildMetadataImportCalls(seed)
		if err != nil {
			return summary, err
		}
		result, err := apply(ctx, id, calls)
		summary.Results[id] = result
		if err != nil {
			summary.Total.Failed++
			return summary, fmt.Errorf("initialize market %s: %w", id, err)
		}
		summary.Total.Applied += result.Applied
	}
	return summary, nil
}

func parseBuiltinMarketIDs(selection string) ([]string, error) {
	selection = strings.TrimSpace(selection)
	if selection == "" || selection == "all" {
		return append([]string(nil), builtinMarketIDs...), nil
	}
	known := make(map[string]struct{}, len(builtinMarketIDs))
	for _, id := range builtinMarketIDs {
		known[id] = struct{}{}
	}
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for _, raw := range strings.Split(selection, ",") {
		id := strings.TrimSpace(raw)
		if _, ok := known[id]; !ok {
			return nil, fmt.Errorf("unknown built-in market %q", id)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("duplicate built-in market %q", id)
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

func builtinMarketConfigRoot() string {
	if value := strings.TrimSpace(os.Getenv("MOOX_COLLECTOR_MARKETS_DIR")); value != "" {
		return value
	}
	return filepath.Join("modules", "collector", "config", "markets")
}

func init() {
	initCmd.AddCommand(initMarketsCmd)
	rootCmd.AddCommand(initCmd)
	initMarketsCmd.Flags().StringVar(&marketInitSelection, "markets", "all", "comma-separated market IDs or all")
	initMarketsCmd.Flags().StringVar(&marketInitMetadataURL, "metadata-url", "", "Storage MetadataService HTTP address")
}
