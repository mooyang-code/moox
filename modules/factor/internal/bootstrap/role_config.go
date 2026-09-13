package bootstrap

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/inputcache"
	"github.com/mooyang-code/moox/modules/factor/internal/trigger"
	"gopkg.in/yaml.v3"
)

type ControlConfig struct {
	Database     DatabaseConfig   `yaml:"database"`
	Storage      StorageConfig    `yaml:"storage"`
	EventBus     CatalogBusConfig `yaml:"eventbus"`
	ArtifactsDir string           `yaml:"artifacts_dir"`
}

type CatalogBusConfig struct {
	URLs           []string `yaml:"urls"`
	CredentialFile string   `yaml:"credential_file"`
}

type EngineApplicationConfig struct {
	EngineID            string                     `yaml:"engine_id"`
	Database            DatabaseConfig             `yaml:"database"`
	Storage             StorageConfig              `yaml:"storage"`
	EventBus            EventBusConfig             `yaml:"eventbus"`
	Engine              EngineConfig               `yaml:"engine"`
	Cache               inputcache.Config          `yaml:"cache"`
	CatalogPollInterval time.Duration              `yaml:"catalog_poll_interval"`
	CatalogSyncTimeout  time.Duration              `yaml:"catalog_sync_timeout"`
	SubjectBatch        trigger.SubjectBatchConfig `yaml:"subject_batch"`
}

func DefaultControlConfig() *ControlConfig {
	shared := Default()
	shared.Database.Path = "./data/factor-control/catalog.db"
	return &ControlConfig{Database: shared.Database, Storage: shared.Storage,
		EventBus: CatalogBusConfig{URLs: shared.EventBus.URLs}, ArtifactsDir: "./data/factor-control/artifacts"}
}

func DefaultEngineApplicationConfig() *EngineApplicationConfig {
	shared := Default()
	shared.Database.Path = "./data/factor-engine/runtime.db"
	shared.Engine.FactorsDir = "./data/factor-engine/artifacts"
	return &EngineApplicationConfig{EngineID: "factor-engine-1", Database: shared.Database,
		Storage: shared.Storage, EventBus: shared.EventBus, Engine: shared.Engine,
		Cache: inputcache.DefaultConfig(), CatalogPollInterval: 30 * time.Second, CatalogSyncTimeout: 10 * time.Minute,
		SubjectBatch: trigger.DefaultSubjectBatchConfig()}
}

func LoadControlConfig(path string) (*ControlConfig, error) {
	cfg := DefaultControlConfig()
	if err := decodeRoleConfig(path, cfg); err != nil {
		return nil, err
	}
	cfg.applyEnv()
	if strings.TrimSpace(cfg.ArtifactsDir) == "" {
		return nil, fmt.Errorf("control artifacts_dir is required")
	}
	if err := validateRoleStorage(cfg.Database, cfg.Storage, cfg.EventBus.URLs); err != nil {
		return nil, err
	}
	return cfg, nil
}

func LoadEngineApplicationConfig(path string) (*EngineApplicationConfig, error) {
	cfg := DefaultEngineApplicationConfig()
	if err := decodeRoleConfig(path, cfg); err != nil {
		return nil, err
	}
	if err := cfg.applyEnv(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(cfg.EngineID) == "" || cfg.CatalogPollInterval <= 0 || cfg.CatalogSyncTimeout <= 0 {
		return nil, fmt.Errorf("engine_id and positive catalog_poll_interval/catalog_sync_timeout are required")
	}
	if err := validateRoleStorage(cfg.Database, cfg.Storage, cfg.EventBus.URLs); err != nil {
		return nil, err
	}
	if err := cfg.Cache.Validate(); err != nil {
		return nil, err
	}
	if err := cfg.SubjectBatch.Validate(); err != nil {
		return nil, err
	}
	if cfg.Engine.PythonWorkers <= 0 || cfg.Engine.ViewReadWorkers <= 0 || cfg.Engine.TaskTimeoutMS <= 0 || cfg.Engine.ViewReadTimeoutMS <= 0 {
		return nil, fmt.Errorf("engine worker counts and timeouts must be positive")
	}
	if strings.TrimSpace(cfg.Engine.PythonBin) == "" || strings.TrimSpace(cfg.Engine.WorkerPath) == "" || strings.TrimSpace(cfg.Engine.FactorsDir) == "" {
		return nil, fmt.Errorf("engine Python paths are required")
	}
	return cfg, nil
}

func validateRoleStorage(database DatabaseConfig, storage StorageConfig, urls []string) error {
	if database.Type != "sqlite" || strings.TrimSpace(database.Path) == "" || database.MaxIdleConns <= 0 || database.MaxOpenConns <= 0 {
		return fmt.Errorf("role requires SQLite path and positive connection limits")
	}
	if len(urls) == 0 {
		return fmt.Errorf("eventbus.urls is required")
	}
	for _, url := range urls {
		if strings.TrimSpace(url) == "" {
			return fmt.Errorf("eventbus URL must not be empty")
		}
	}
	return (&Config{Storage: storage}).validateStorageTargets()
}

func (c *ControlConfig) applyEnv() {
	applyDatabaseEnv(&c.Database)
	applyStorageEnv(&c.Storage)
	applyCatalogBusEnv(&c.EventBus)
	if v := strings.TrimSpace(os.Getenv("MOOX_FACTOR_ARTIFACTS_DIR")); v != "" {
		c.ArtifactsDir = v
	} else if v := strings.TrimSpace(os.Getenv("MOOX_FACTOR_ENGINE_FACTORS_DIR")); v != "" {
		c.ArtifactsDir = v
	}
}

func (c *EngineApplicationConfig) applyEnv() error {
	if v := strings.TrimSpace(os.Getenv("MOOX_FACTOR_ENGINE_DB_PATH")); v != "" {
		c.Database.Path = v
	}
	applyStorageEnv(&c.Storage)
	if v, ok := os.LookupEnv("MOOX_EVENTBUS_NATS_URL"); ok {
		c.EventBus.URLs = splitEventBusURLs(v)
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE")); v != "" {
		c.EventBus.CredentialFile = v
	} else if v := strings.TrimSpace(os.Getenv("MOOX_EVENTBUS_CREDENTIAL_FILE")); v != "" {
		c.EventBus.CredentialFile = v
	}
	shared := &Config{Engine: c.Engine}
	if err := shared.applyEnv(); err != nil {
		return err
	}
	c.Engine = shared.Engine
	if v := strings.TrimSpace(os.Getenv("MOOX_FACTOR_ENGINE_ID")); v != "" {
		c.EngineID = v
	}
	return nil
}

func applyDatabaseEnv(c *DatabaseConfig) {
	if v := os.Getenv("MOOX_FACTOR_DB_PATH"); v != "" {
		c.Path = v
	}
}

func applyStorageEnv(c *StorageConfig) {
	if v := os.Getenv("MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET"); v != "" {
		c.GatewayTarget = v
	}
	if v := os.Getenv("MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID"); v != "" {
		c.GatewayNodeID = v
	}
	if v := os.Getenv("MOOX_FACTOR_STORAGE_RPC_KEY_ID"); v != "" {
		c.KeyID = v
	}
	if v := os.Getenv("MOOX_FACTOR_STORAGE_RPC_HMAC_KEY_FILE"); v != "" {
		c.HMACKeyFile = v
	}
}

func applyCatalogBusEnv(c *CatalogBusConfig) {
	if v, ok := os.LookupEnv("MOOX_EVENTBUS_NATS_URL"); ok {
		c.URLs = splitEventBusURLs(v)
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE")); v != "" {
		c.CredentialFile = v
	} else if v := strings.TrimSpace(os.Getenv("MOOX_EVENTBUS_CREDENTIAL_FILE")); v != "" {
		c.CredentialFile = v
	}
}

func decodeRoleConfig(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode role config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("role config must contain exactly one YAML document")
	}
	return nil
}
