package cmd

import (
	"fmt"
	"os"
	"path/filepath"
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
			ZipPath: collectorMarketsFlags.ZipPath, SpaceID: manifest.SpaceID, PackageName: manifest.SCF.FunctionName,
			Runtime: "Go1", Handler: "main", PackageType: "data_collector", BizType: "data_collector", NodeType: "scf-event",
			ServiceAccessKey: os.Getenv("MOOX_SERVICE_AUTH_ACCESS_KEY"), ServiceSecretKey: os.Getenv("MOOX_SERVICE_AUTH_SECRET_KEY"),
			Env: []string{"MOOX_SPACE_ID=" + manifest.SpaceID}, Config: []string{fmt.Sprintf("timeout_seconds=%d", manifest.SCF.TimeoutSeconds)},
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
	collectorFunctionCmd.AddCommand(collectorFunctionPublishMarketsCmd)
	f := collectorFunctionPublishMarketsCmd.Flags()
	f.StringVar(&collectorMarketsFlags.ManifestDir, "manifest-dir", "", "market manifest directory")
	f.StringVar(&collectorMarketsFlags.Environment, "environment", "", "validation environment")
	f.StringVar(&collectorMarketsFlags.ControlURL, "control-url", "", "Control service base URL")
	f.StringVar(&collectorMarketsFlags.CloudAccountID, "cloud-account-id", "", "cloud account id")
	f.StringVar(&collectorMarketsFlags.Region, "region", "", "cloud region")
	f.StringVar(&collectorMarketsFlags.ZipPath, "zip", "", "prebuilt SCF zip")
	f.StringVar(&collectorMarketsFlags.Version, "version", "dev", "package version")
}
