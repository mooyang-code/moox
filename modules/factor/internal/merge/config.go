package merge

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
	"gopkg.in/yaml.v3"
)

type EventBusConfig struct {
	URLs           []string      `yaml:"urls"`
	CredentialFile string        `yaml:"credential_file"`
	FetchMaxWait   time.Duration `yaml:"fetch_max_wait"`
}

type StorageConfig struct {
	GatewayTarget string `yaml:"gateway_target"`
	GatewayNodeID string `yaml:"gateway_node_id"`
	KeyID         string `yaml:"key_id"`
	HMACKeyFile   string `yaml:"hmac_key_file"`
}

type DatabaseConfig struct {
	Path string `yaml:"path"`
}

type datasetSpec struct {
	DatasetID        string                    `yaml:"dataset_id"`
	SpaceID          string                    `yaml:"space_id"`
	Frequency        string                    `yaml:"frequency"`
	MergeMode        string                    `yaml:"merge_mode"`
	ConfigSnapshotID string                    `yaml:"config_snapshot_id"`
	KeyContract      domain.KeyContract        `yaml:"key_contract"`
	ObjectSet        []string                  `yaml:"object_set"`
	UniverseSource   string                    `yaml:"universe_source"`
	Sources          []domain.SourceDatasetRef `yaml:"sources"`
	FieldMappings    []domain.FieldMapping     `yaml:"field_mappings"`
}

type ProcessConfig struct {
	MergeID     string                 `yaml:"merge_id"`
	Database    DatabaseConfig         `yaml:"database"`
	Storage     StorageConfig          `yaml:"storage"`
	EventBus    EventBusConfig         `yaml:"eventbus"`
	Definitions []domain.MergedDataset `yaml:"-"`
	Specs       []datasetSpec          `yaml:"definitions"`
}

func LoadProcessConfig(path string) (ProcessConfig, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ProcessConfig{}, err
	}
	cfg := ProcessConfig{
		MergeID:  "factor-merge-1",
		Database: DatabaseConfig{Path: "./data/factor-merge/merge.db"},
		Storage:  StorageConfig{GatewayTarget: "ip://127.0.0.1:11003", KeyID: "merge"},
		EventBus: EventBusConfig{URLs: []string{"nats://127.0.0.1:4222"}, FetchMaxWait: time.Second},
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&cfg); err != nil {
		return ProcessConfig{}, fmt.Errorf("decode merge config: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return ProcessConfig{}, fmt.Errorf("merge config must contain exactly one YAML document")
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_FACTOR_MERGE_DB_PATH")); v != "" {
		cfg.Database.Path = v
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_RPC_GATEWAY_TARGET")); v != "" {
		cfg.Storage.GatewayTarget = v
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_RPC_GATEWAY_NODE_ID")); v != "" {
		cfg.Storage.GatewayNodeID = v
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_RPC_KEY_ID")); v != "" {
		cfg.Storage.KeyID = v
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_FACTOR_STORAGE_RPC_HMAC_KEY_FILE")); v != "" {
		cfg.Storage.HMACKeyFile = v
	}
	if v := strings.TrimSpace(os.Getenv("MOOX_FACTOR_EVENTBUS_CREDENTIAL_FILE")); v != "" {
		cfg.EventBus.CredentialFile = v
	}
	if v, ok := os.LookupEnv("MOOX_EVENTBUS_NATS_URL"); ok && strings.TrimSpace(v) != "" {
		cfg.EventBus.URLs = strings.Split(v, ",")
	}
	if strings.TrimSpace(cfg.MergeID) == "" {
		return ProcessConfig{}, fmt.Errorf("merge_id is required")
	}
	if strings.TrimSpace(cfg.Database.Path) == "" {
		return ProcessConfig{}, fmt.Errorf("merge database path is required")
	}
	if cfg.EventBus.FetchMaxWait <= 0 {
		cfg.EventBus.FetchMaxWait = time.Second
	}
	for _, spec := range cfg.Specs {
		def := spec.toDomain()
		if len(def.FieldMappings) == 0 {
			for _, source := range def.Sources {
				for _, field := range source.Fields {
					def.FieldMappings = append(def.FieldMappings, domain.FieldMapping{
						SourceDatasetID: source.DatasetID, SourceField: field,
						TargetField: domain.MappedSourceField(source.DatasetID, field),
					})
				}
			}
		}
		if err := domain.ValidateMergedDataset(def); err != nil {
			return ProcessConfig{}, err
		}
		cfg.Definitions = append(cfg.Definitions, def)
	}
	if len(cfg.Definitions) == 0 {
		return ProcessConfig{}, fmt.Errorf("merge definitions are required")
	}
	return cfg, nil
}

func (s datasetSpec) toDomain() domain.MergedDataset {
	return domain.MergedDataset{
		DatasetID: s.DatasetID, SpaceID: s.SpaceID, Frequency: s.Frequency, MergeMode: s.MergeMode,
		ConfigSnapshotID: s.ConfigSnapshotID, KeyContract: s.KeyContract, ObjectSet: append([]string(nil), s.ObjectSet...),
		UniverseSource: s.UniverseSource, Sources: append([]domain.SourceDatasetRef(nil), s.Sources...), FieldMappings: append([]domain.FieldMapping(nil), s.FieldMappings...),
	}
}
