package command

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/cli/internal/config"
	doctorcli "github.com/mooyang-code/moox/modules/cli/internal/doctor"
	"github.com/mooyang-code/moox/modules/cli/internal/doctorclient"
	pb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"github.com/mooyang-code/moox/packages/commonpb"
	core "github.com/mooyang-code/moox/packages/doctor"
	"github.com/mooyang-code/moox/packages/report"
	"github.com/mooyang-code/moox/packages/security"
	"github.com/spf13/cobra"
	"trpc.group/trpc-go/trpc-go/client"
)

type doctorExitError struct{ code int }

func (e doctorExitError) Error() string { return fmt.Sprintf("doctor exited with status %d", e.code) }
func (e doctorExitError) ExitCode() int { return e.code }

type doctorCommandDeps struct {
	loadConfig        func() (*config.Config, error)
	newClient         func(string, string) *doctorclient.Client
	newMetadataClient func(string, string) doctorcli.StorageActivationClient
}

func init() {
	rootCmd.AddCommand(newDoctorCommand(doctorCommandDeps{
		loadConfig:        config.LoadConfig,
		newClient:         doctorclient.New,
		newMetadataClient: newSignedStorageMetadataClient,
	}))
}

func newDoctorCommand(deps doctorCommandDeps) *cobra.Command {
	cmd := &cobra.Command{Use: "doctor", Short: "Run bounded MooX deployment checks", SilenceUsage: true}
	cmd.AddCommand(newDoctorModeCommand("bootstrap", deps), newDoctorModeCommand("diagnose", deps))
	return cmd
}

func newDoctorModeCommand(mode string, deps doctorCommandDeps) *cobra.Command {
	var nodeID, format, output string
	var checks []string
	cmd := &cobra.Command{
		Use:          mode,
		Short:        map[string]string{"bootstrap": "Check a new local deployment", "diagnose": "Interpret bounded Monitor facts"}[mode],
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			return validateDoctorFlags(format, output, checks)
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := deps.loadConfig()
			if err != nil {
				cfg = &config.Config{}
			}
			doctorCfg := cfg.EffectiveDoctor()
			if nodeID == "" {
				nodeID = doctorCfg.NodeID
			}
			client := deps.newClient(doctorCfg.MonitorTarget, doctorCfg.SysDeployTarget)
			metadataClientFactory := deps.newMetadataClient
			if metadataClientFactory == nil {
				metadataClientFactory = newSignedStorageMetadataClient
			}
			storageActivation := metadataClientFactory(defaultMetadataImportURL(""), os.Getenv("MOOX_STORAGE_NODE_AUTH_SECRET"))
			auth, err := loadDoctorHealthAuth(doctorCfg.ReleaseRoot)
			if err != nil {
				return err
			}
			prober := doctorcli.HTTPProber{Auth: auth}
			var reportValue core.Report
			switch mode {
			case "bootstrap":
				reportValue, err = doctorcli.RunBootstrap(cmd.Context(), doctorcli.BootstrapOptions{
					NodeID: nodeID, LocalNodeID: doctorCfg.NodeID, ReleaseRoot: doctorCfg.ReleaseRoot,
					SeedPath: resolveReleasePath(doctorCfg.ReleaseRoot, doctorCfg.SeedPath), PipelinePath: resolveReleasePath(doctorCfg.ReleaseRoot, doctorCfg.PipelinePath),
					CheckIDs: checks, Client: client, StorageActivation: storageActivation, Prober: prober,
				})
			case "diagnose":
				pipelines, loadErr := report.LoadPipelineAllowlist(resolveReleasePath(doctorCfg.ReleaseRoot, doctorCfg.PipelinePath))
				if loadErr != nil {
					return loadErr
				}
				reportValue, err = doctorcli.RunDiagnose(cmd.Context(), doctorcli.DiagnoseOptions{NodeID: nodeID, CheckIDs: checks, Client: client, Prober: prober, Pipelines: pipelines})
			}
			if err != nil {
				return err
			}
			rendered, err := doctorcli.Render(reportValue, format)
			if err != nil {
				return err
			}
			if output != "" {
				if err := doctorcli.WriteAtomic(output, rendered); err != nil {
					return err
				}
			} else if _, err := cmd.OutOrStdout().Write(rendered); err != nil {
				return err
			}
			if code := doctorExitCode(reportValue.Conclusion); code != 0 {
				return doctorExitError{code: code}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&nodeID, "node", "", "target node ID")
	cmd.Flags().StringSliceVar(&checks, "check", nil, "specific bounded check ID")
	cmd.Flags().StringVar(&format, "format", "json", "report format: json, text, or markdown")
	cmd.Flags().StringVar(&output, "output", "", "atomically write the report to this path")
	return cmd
}

type signedStorageMetadataClient struct {
	proxy pb.MetadataClientProxy
	auth  *commonpb.AuthInfo
}

func newSignedStorageMetadataClient(target, secret string) doctorcli.StorageActivationClient {
	return &signedStorageMetadataClient{
		proxy: pb.NewMetadataClientProxy(client.WithTarget(target), client.WithProtocol("http"), client.WithNetwork("tcp")),
		auth:  &commonpb.AuthInfo{AppId: "storage-metadata", AppKey: security.HMACSHA256Hex(secret, []byte("storage-metadata"))},
	}
}

func (c *signedStorageMetadataClient) ListDatasets(ctx context.Context, req *pb.ListDatasetsReq) (*pb.ListDatasetsRsp, error) {
	if c == nil || c.proxy == nil {
		return nil, errors.New("storage metadata client is unavailable")
	}
	request := &pb.ListDatasetsReq{}
	if req != nil {
		*request = *req
	}
	request.AuthInfo = c.auth
	return c.proxy.ListDatasets(ctx, request)
}

func (c *signedStorageMetadataClient) CheckDatasetActivation(ctx context.Context, req *pb.CheckDatasetActivationReq) (*pb.CheckDatasetActivationRsp, error) {
	if c == nil || c.proxy == nil {
		return nil, errors.New("storage metadata client is unavailable")
	}
	request := &pb.CheckDatasetActivationReq{}
	if req != nil {
		*request = *req
	}
	request.AuthInfo = c.auth
	return c.proxy.CheckDatasetActivation(ctx, request)
}

func validateDoctorFlags(format, output string, checks []string) error {
	if format != "json" && format != "text" && format != "markdown" {
		return fmt.Errorf("format must be json, text, or markdown")
	}
	if len(checks) > core.MaxSelectedChecks {
		return fmt.Errorf("check selection exceeds %d", core.MaxSelectedChecks)
	}
	seen := map[string]bool{}
	for _, check := range checks {
		if strings.TrimSpace(check) == "" || seen[check] {
			return fmt.Errorf("check IDs must be non-empty and unique")
		}
		seen[check] = true
	}
	if output != "" {
		info, err := os.Stat(filepath.Dir(output))
		if err != nil || !info.IsDir() {
			return fmt.Errorf("output directory must already exist")
		}
	}
	return nil
}

func doctorExitCode(conclusion core.Conclusion) int {
	switch conclusion {
	case core.ConclusionHealthy:
		return 0
	case core.ConclusionDegraded:
		return 1
	case core.ConclusionUnhealthy:
		return 2
	default:
		return 3
	}
}

func resolveReleasePath(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func loadDoctorHealthAuth(releaseRoot string) (doctorcli.HealthAuth, error) {
	auth := doctorcli.HealthAuth{Version: envOr("MOOX_HEALTH_AUTH_VERSION", "moox-health-v1"), AccessKey: envOr("MOOX_HEALTH_AUTH_ACCESS_KEY", "monitor"), SecretKey: os.Getenv("MOOX_HEALTH_AUTH_SECRET_KEY")}
	if auth.SecretKey != "" {
		return auth, nil
	}
	path := filepath.Join(releaseRoot, "secrets", "health-auth.env")
	info, statErr := os.Lstat(path)
	if statErr == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0) {
		return auth, fmt.Errorf("health auth file must be a regular 0600 file")
	}
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return auth, nil
		}
		return auth, err
	}
	defer file.Close()
	if err := readHealthEnv(file, &auth); err != nil {
		return auth, err
	}
	return auth, nil
}

func readHealthEnv(reader io.Reader, auth *doctorcli.HealthAuth) error {
	scanner := bufio.NewScanner(io.LimitReader(reader, 64<<10))
	for scanner.Scan() {
		name, value, ok := strings.Cut(scanner.Text(), "=")
		if !ok {
			continue
		}
		switch name {
		case "MOOX_HEALTH_AUTH_VERSION":
			auth.Version = value
		case "MOOX_HEALTH_AUTH_ACCESS_KEY":
			auth.AccessKey = value
		case "MOOX_HEALTH_AUTH_SECRET_KEY":
			auth.SecretKey = value
		}
	}
	return scanner.Err()
}

func envOr(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}
