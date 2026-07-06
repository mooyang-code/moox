package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mooyang-code/moox/modules/storage/internal/bootstrap/metadata"
	storageconfig "github.com/mooyang-code/moox/modules/storage/internal/config"
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
	Spaces             int `json:"spaces"`
	DataSources        int `json:"data_sources"`
	Subjects           int `json:"subjects"`
	SubjectSymbols     int `json:"subject_symbols"`
	Datasets           int `json:"datasets"`
	DatasetSubjects    int `json:"dataset_subjects"`
	Fields             int `json:"fields"`
	Factors            int `json:"factors"`
	DatasetColumns     int `json:"dataset_columns"`
	Views              int `json:"views"`
	ViewColumns        int `json:"view_columns"`
	PrimaryStoreNodes  int `json:"primary_store_nodes"`
	Devices            int `json:"devices"`
	PrimaryStoreRoutes int `json:"primary_store_routes"`
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
		return fmt.Errorf("expected command: init or import-seed")
	}
	switch args[0] {
	case "init":
		return runInit(args[1:], stdout, stderr)
	case "import-seed":
		return runImportSeed(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("unknown command %q: use init or import-seed", args[0])
	}
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
	if err := metadata.InitSchema(context.Background(), metadata.SchemaOptions{
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
	result, err := metadata.ImportSeed(context.Background(), metadata.SeedOptions{
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
			Spaces:             result.Spaces,
			DataSources:        result.DataSources,
			Subjects:           result.Subjects,
			SubjectSymbols:     result.SubjectSymbols,
			Datasets:           result.Datasets,
			DatasetSubjects:    result.DatasetSubjects,
			Fields:             result.Fields,
			Factors:            result.Factors,
			DatasetColumns:     result.DatasetColumns,
			Views:              result.Views,
			ViewColumns:        result.ViewColumns,
			PrimaryStoreNodes:  result.PrimaryStoreNodes,
			Devices:            result.Devices,
			PrimaryStoreRoutes: result.PrimaryStoreRoutes,
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
	var cfg storageconfig.RuntimeConfig
	if configPath != "" {
		dir := filepath.Dir(configPath)
		file := filepath.Base(configPath)
		if err := storageconfig.NewConfigLoader(dir).LoadConfigWithDefaults(file, &cfg, cfg.ApplyDefaults); err != nil {
			return cfg.Storage, fmt.Errorf("load storage config %s: %w", configPath, err)
		}
	} else {
		cfg.ApplyDefaults()
	}
	if root := os.Getenv("MOOX_STORAGE_HOME"); root != "" {
		cfg.Storage.Root = root
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

func resolveSeedPath(path string, storageConfigPath string) string {
	if path != "" {
		return path
	}
	if path := os.Getenv("STORAGE_SEED_FILE"); path != "" {
		return path
	}
	if storageConfigPath != "" {
		return filepath.Join(filepath.Dir(storageConfigPath), "metadata.seed.yaml")
	}
	return filepath.Join("config", "metadata.seed.yaml")
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
