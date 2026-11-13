package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/bootstrap/metadata"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
	trpc "trpc.group/trpc-go/trpc-go"
)

type cliResult struct {
	Module  string      `json:"module"`
	Action  string      `json:"action"`
	Status  string      `json:"status"`
	DBPath  string      `json:"db_path"`
	Seed    string      `json:"seed_path,omitempty"`
	Summary interface{} `json:"summary,omitempty"`
}

type importSummary struct {
	Spaces         int `json:"spaces"`
	DataSources    int `json:"data_sources"`
	Subjects       int `json:"subjects"`
	Tags           int `json:"tags"`
	Datasets       int `json:"datasets"`
	Fields         int `json:"fields"`
	DatasetColumns int `json:"dataset_columns"`
	Views          int `json:"views"`
	ViewColumns    int `json:"view_columns"`
	Devices        int `json:"devices"`
}

type operationResult struct {
	Module  string      `json:"module"`
	Action  string      `json:"action"`
	Status  string      `json:"status"`
	Summary interface{} `json:"summary"`
}

func main() {
	if err := runCommand(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		printError(os.Stderr, err)
		os.Exit(exitCode(err))
	}
}

func runCommand(args []string, stdout io.Writer, stderr io.Writer) error {
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	if len(args) == 0 {
		return fmt.Errorf("expected command: init, import-seed, repair-view, reset-metadata, or delete-view")
	}
	switch args[0] {
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "import-seed":
		return runImportSeed(args[1:], stdout, stderr)
	case "repair-view":
		return runRepairView(args[1:], stdout, stderr)
	case "reset-metadata":
		return runResetMetadata(args[1:], stdout, stderr)
	case "delete-view":
		return runDeleteView(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q: use init, import-seed, repair-view, reset-metadata, or delete-view", args[0])
	}
}

func writeOperationResult(stdout io.Writer, result operationResult) error {
	return json.NewEncoder(stdout).Encode(result)
}

func runInit(args []string, stdout io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := commandOptions{}
	registerCommonFlags(fs, &opts)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected init arguments: %s", strings.Join(fs.Args(), " "))
	}
	storage, err := loadStorage(opts.storageConf)
	if err != nil {
		return err
	}
	schemaPath := resolveSchemaPath(opts.schemaPath)
	if err := metadata.InitSchema(trpc.BackgroundContext(), metadata.SchemaOptions{
		Storage:    storage,
		SchemaPath: schemaPath,
	}); err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(cliResult{
		Module: "storage",
		Action: "init",
		Status: "ok",
		DBPath: metadataDBPath(storage),
	})
}

func runImportSeed(args []string, stdout io.Writer, stderr io.Writer) error {
	fs := flag.NewFlagSet("import-seed", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	opts := commandOptions{}
	registerCommonFlags(fs, &opts)
	fs.StringVar(&opts.seedPath, "seed", "", "metadata seed yaml path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("unexpected import-seed arguments: %s", strings.Join(fs.Args(), " "))
	}
	storage, err := loadStorage(opts.storageConf)
	if err != nil {
		return err
	}
	seedPath := resolveSeedPath(opts.seedPath, opts.storageConf)
	result, err := metadata.ImportSeed(trpc.BackgroundContext(), metadata.SeedOptions{
		Storage:    storage,
		SchemaPath: resolveSchemaPath(opts.schemaPath),
		SeedPath:   seedPath,
	})
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(cliResult{
		Module: "storage",
		Action: "import-seed",
		Status: "ok",
		DBPath: metadataDBPath(storage),
		Seed:   seedPath,
		Summary: importSummary{
			Spaces:         result.Spaces,
			DataSources:    result.DataSources,
			Subjects:       result.Subjects,
			Tags:           result.Tags,
			Datasets:       result.Datasets,
			Fields:         result.Fields,
			DatasetColumns: result.DatasetColumns,
			Views:          result.Views,
			ViewColumns:    result.ViewColumns,
			Devices:        result.Devices,
		},
	})
}

type commandOptions struct {
	storageConf string
	schemaPath  string
	seedPath    string
}

func registerCommonFlags(fs *flag.FlagSet, opts *commandOptions) {
	fs.StringVar(&opts.storageConf, "storage-conf", defaultStorageConfigPath(), "storage business config path")
	fs.StringVar(&opts.schemaPath, "schema-path", "", "metadata schema sql path")
}

func loadStorage(configPath string) (storageconfig.StorageConfig, error) {
	cfg, err := loadRuntimeConfig(configPath)
	if err != nil {
		return cfg.Storage, err
	}
	if root := os.Getenv("MOOX_STORAGE_HOME"); root != "" {
		cfg.Storage.ApplyHomeRoot(root)
	}
	return cfg.Storage, nil
}

func metadataDBPath(storage storageconfig.StorageConfig) string {
	root := storage.Root
	if root == "" {
		root = "var/storage"
	}
	if storage.Metadata.Path != "" {
		return storage.Metadata.Path
	}
	return filepath.Join(root, "metadata", "storage_metadata.db")
}

func defaultStorageConfigPath() string {
	if path := os.Getenv("MOOX_STORAGE_CONFIG"); path != "" {
		return path
	}
	if path := os.Getenv("STORAGE_APP_CONFIG"); path != "" {
		return path
	}
	if dir := os.Getenv("STORAGE_CONFIG_PATH"); dir != "" {
		return filepath.Join(dir, "storage.yaml")
	}
	return filepath.Join("config", "storage.yaml")
}

func resolveSchemaPath(path string) string {
	if path != "" {
		return path
	}
	if path := os.Getenv("STORAGE_SCHEMA_FILE"); path != "" {
		return path
	}
	candidates := []string{
		filepath.Join("schema", "metadata.sql"),
		filepath.Join("modules", "storage", "schema", "metadata.sql"),
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	return candidates[0]
}

func resolveSeedPath(path string, _ string) string {
	if path != "" {
		return path
	}
	if path := os.Getenv("STORAGE_SEED_FILE"); path != "" {
		return path
	}
	return ""
}

func printError(stderr io.Writer, err error) {
	if stderr == nil {
		stderr = io.Discard
	}
	_ = json.NewEncoder(stderr).Encode(map[string]string{
		"error":   "storage_cli_failed",
		"message": err.Error(),
	})
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	return 1
}

func loadRuntimeConfig(configPath string) (storageconfig.RuntimeConfig, error) {
	var cfg storageconfig.RuntimeConfig
	if configPath == "" {
		cfg.ApplyDefaults()
		return cfg, nil
	}
	if err := storageconfig.NewConfigLoader(filepath.Dir(configPath)).LoadConfigWithDefaults(filepath.Base(configPath), &cfg, cfg.ApplyDefaults); err != nil {
		return cfg, fmt.Errorf("load storage config %s: %w", configPath, err)
	}
	return cfg, nil
}
