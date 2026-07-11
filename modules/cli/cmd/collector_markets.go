package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/marketmanifest"
	"github.com/spf13/cobra"
)

var collectorMarketsFlags struct {
	ManifestDir, Environment, ControlURL, CloudAccountID, Region, ZipPath, Version string
}

type collectorMarketPublishResult struct {
	MarketID string                   `json:"market_id"`
	Status   string                   `json:"status"`
	Publish  *collectorPublishSummary `json:"publish,omitempty"`
}

var collectorFunctionPublishMarketsCmd = &cobra.Command{
	Use:   "publish-markets",
	Short: "Publish every readiness-enabled built-in market SCF",
	RunE: func(cmd *cobra.Command, _ []string) error {
		results, err := publishCollectorMarkets(cmd, collectorMarketsFlags.ManifestDir, collectorMarketsFlags.Environment)
		if err != nil {
			return err
		}
		return writeJSON(cmd, results)
	},
}

var collectorMarketsVerifyFlags struct{ ManifestDir, ControlURL string }

var collectorFunctionVerifyMarketsCmd = &cobra.Command{
	Use:   "verify-markets",
	Short: "Verify exact runtime-enabled Market SCF identities",
	RunE: func(cmd *cobra.Command, _ []string) error {
		manifests, err := marketmanifest.LoadDir(collectorMarketsVerifyFlags.ManifestDir)
		if err != nil {
			return err
		}
		results := []map[string]any{}
		for _, manifest := range manifests {
			if !manifest.RuntimeEnabled {
				continue
			}
			client := newControlClient(collectorMarketsVerifyFlags.ControlURL, "", os.Getenv("MOOX_SERVICE_AUTH_ACCESS_KEY"), os.Getenv("MOOX_SERVICE_AUTH_SECRET_KEY"), manifest.SpaceID)
			nodes, _, err := client.ListNodesWithTag(cmd.Context(), 1, 200, "__verify_scf__")
			if err != nil {
				return err
			}
			found := false
			for _, node := range nodes {
				if node.NodeID == manifest.SCF.FunctionName && node.FunctionName == manifest.SCF.FunctionName {
					actual, ok := node.Metadata["actual_scf"].(map[string]any)
					spaceMatches := ok && nestedString(actual, "environment", "MOOX_SPACE_ID") == manifest.SpaceID
					if !ok || fmt.Sprint(actual["runtime"]) != "Go1" || fmt.Sprint(actual["handler"]) != "main" || int64Value(actual["timeout"]) != manifest.SCF.TimeoutSeconds || !spaceMatches || !strings.EqualFold(fmt.Sprint(actual["status"]), "Active") {
						return fmt.Errorf("market %s SCF actual configuration does not match manifest: runtime=%q handler=%q timeout=%d status=%q space_matches=%t", manifest.MarketID, fmt.Sprint(actual["runtime"]), fmt.Sprint(actual["handler"]), int64Value(actual["timeout"]), fmt.Sprint(actual["status"]), spaceMatches)
					}
					found = true
					break
				}
			}
			if !found {
				return fmt.Errorf("market %s exact SCF %s is not registered in space %s", manifest.MarketID, manifest.SCF.FunctionName, manifest.SpaceID)
			}
			results = append(results, map[string]any{"market_id": manifest.MarketID, "space_id": manifest.SpaceID, "function_name": manifest.SCF.FunctionName, "status": "verified"})
		}
		if len(results) == 0 {
			return fmt.Errorf("no runtime-enabled market SCF to verify")
		}
		return writeJSON(cmd, results)
	},
}

func int64Value(value any) int64 {
	switch typed := value.(type) {
	case float64:
		return int64(typed)
	case int64:
		return typed
	case int:
		return int64(typed)
	}
	return 0
}

func nestedString(values map[string]any, object, key string) string {
	nested, _ := values[object].(map[string]any)
	return fmt.Sprint(nested[key])
}

func publishCollectorMarkets(cmd *cobra.Command, manifestDir, environment string) ([]collectorMarketPublishResult, error) {
	if manifestDir == "" || environment == "" || collectorMarketsFlags.ControlURL == "" || collectorMarketsFlags.CloudAccountID == "" || collectorMarketsFlags.Region == "" || collectorMarketsFlags.ZipPath == "" {
		return nil, fmt.Errorf("--manifest-dir, --environment, --control-url, --cloud-account-id, --region and --zip are required")
	}
	manifests, err := marketmanifest.LoadDir(manifestDir)
	if err != nil {
		return nil, err
	}
	results := make([]collectorMarketPublishResult, 0, len(manifests))
	for _, manifest := range manifests {
		if !manifest.RuntimeEnabled {
			results = append(results, collectorMarketPublishResult{MarketID: manifest.MarketID, Status: "skipped_not_runtime_enabled"})
			continue
		}
		evidence, err := marketmanifest.LoadValidationFile(filepath.Join(manifestDir, manifest.MarketID, "provider-validation.yaml"))
		if err != nil {
			return nil, err
		}
		validUntil, err := time.Parse(time.RFC3339, evidence.ValidUntil)
		if err != nil || !validUntil.After(time.Now().UTC()) || evidence.Environment != environment || !evidence.CapabilityEnabled || !evidence.Network.Reachable {
			return nil, fmt.Errorf("market %s does not have current enabled %s evidence", manifest.MarketID, environment)
		}
		opts := collectorPublishOptions{
			collectorPackageOptions: collectorPackageOptions{Version: collectorMarketsFlags.Version},
			ControlURL:              collectorMarketsFlags.ControlURL, CloudAccountID: collectorMarketsFlags.CloudAccountID, Region: collectorMarketsFlags.Region,
			ZipPath: collectorMarketsFlags.ZipPath, SpaceID: manifest.SpaceID, PackageName: "moox-collector", FunctionName: manifest.SCF.FunctionName,
			Runtime: "Go1", Handler: "main", PackageType: "data_collector", BizType: "data_collector", NodeType: "scf-event",
			ServiceAccessKey: os.Getenv("MOOX_SERVICE_AUTH_ACCESS_KEY"), ServiceSecretKey: os.Getenv("MOOX_SERVICE_AUTH_SECRET_KEY"),
			Env: []string{"MOOX_SPACE_ID=" + manifest.SpaceID}, Config: []string{fmt.Sprintf("timeout=%d", manifest.SCF.TimeoutSeconds), "memory_size=256"},
		}
		if manifest.MarketID == "stock_cn" && os.Getenv("MOOX_TDX_ADDRESS") != "" {
			opts.Env = append(opts.Env, "MOOX_TDX_ADDRESS="+os.Getenv("MOOX_TDX_ADDRESS"))
		}
		summary, err := publishCollectorFunction(cmd.Context(), opts)
		if err != nil {
			return results, fmt.Errorf("publish market %s: %w", manifest.MarketID, err)
		}
		results = append(results, collectorMarketPublishResult{MarketID: manifest.MarketID, Status: "published", Publish: &summary})
	}
	return results, nil
}

func init() {
	collectorFunctionCmd.AddCommand(collectorFunctionPublishMarketsCmd, collectorFunctionVerifyMarketsCmd)
	f := collectorFunctionPublishMarketsCmd.Flags()
	f.StringVar(&collectorMarketsFlags.ManifestDir, "manifest-dir", "", "market manifest directory")
	f.StringVar(&collectorMarketsFlags.Environment, "environment", "", "validation environment")
	f.StringVar(&collectorMarketsFlags.ControlURL, "control-url", "", "Control service base URL")
	f.StringVar(&collectorMarketsFlags.CloudAccountID, "cloud-account-id", "", "cloud account id")
	f.StringVar(&collectorMarketsFlags.Region, "region", "", "cloud region")
	f.StringVar(&collectorMarketsFlags.ZipPath, "zip", "", "prebuilt SCF zip")
	f.StringVar(&collectorMarketsFlags.Version, "version", "dev", "package version")
	v := collectorFunctionVerifyMarketsCmd.Flags()
	v.StringVar(&collectorMarketsVerifyFlags.ManifestDir, "manifest-dir", "", "market manifest directory")
	v.StringVar(&collectorMarketsVerifyFlags.ControlURL, "control-url", "", "Control service base URL")
	_ = collectorFunctionVerifyMarketsCmd.MarkFlagRequired("manifest-dir")
	_ = collectorFunctionVerifyMarketsCmd.MarkFlagRequired("control-url")
}
