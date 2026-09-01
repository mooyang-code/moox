package command

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
	"google.golang.org/protobuf/proto"
)

const (
	accessServiceName   = "trpc.moox.storage.PrimaryStore"
	metadataServiceName = "trpc.moox.storage.Metadata"
)

var (
	metadataImportFile        string
	metadataImportURL         string
	metadataImportDryRun      bool
	metadataImportIfNotExists bool
	metadataImportSpaces      []string
	metadataSpacesFile        string
	metadataApplyFile         string
	metadataApplyURL          string
	metadataApplyDryRun       bool
)

var metadataCmd = &cobra.Command{
	Use:   "metadata",
	Short: "存储元数据管理工具",
}

var metadataImportCmd = &cobra.Command{
	Use:   "import",
	Short: "导入存储元数据 seed",
	Long: `通过 moox-storage MetadataService 导入存储元数据 seed。

示例:
  moox-cli metadata import --file ../../examples/setup/default/metadata.yaml --metadata-url http://127.0.0.1:20200 --if-not-exists
  moox-cli metadata import --file ../../examples/setup/default/metadata.yaml --spaces crypto_market --dry-run`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(metadataImportFile) == "" {
			return fmt.Errorf("必须指定 --file")
		}
		seed, err := loadMetadataSeed(metadataImportFile)
		if err != nil {
			return err
		}
		seed, err = selectMetadataSpaces(seed, metadataImportSpaces)
		if err != nil {
			return err
		}
		calls, err := buildMetadataImportCalls(seed)
		if err != nil {
			return err
		}
		url := defaultMetadataImportURL(metadataImportURL)
		if metadataImportDryRun {
			return writeMetadataImportSummary(metadataImportSummary{
				Status:      "dry_run",
				DryRun:      true,
				MetadataURL: url,
				Planned:     len(calls),
				Resources:   countMetadataCalls(calls),
			})
		}
		summary, err := runMetadataImport(cmd.Context(), url, calls, metadataImportIfNotExists)
		if err != nil {
			return err
		}
		return writeMetadataImportSummary(summary)
	},
}

var metadataSpacesCmd = &cobra.Command{
	Use:   "spaces",
	Short: "列出 metadata seed 可导入的业务空间",
	RunE: func(cmd *cobra.Command, _ []string) error {
		if strings.TrimSpace(metadataSpacesFile) == "" {
			return fmt.Errorf("必须指定 --file")
		}
		seed, err := loadMetadataSeed(metadataSpacesFile)
		if err != nil {
			return err
		}
		return writeSetupJSON(cmd, map[string]any{"spaces": metadataSpaceCatalog(seed)})
	},
}

var metadataApplyCmd = &cobra.Command{
	Use:   "apply",
	Short: "创建并校验存储元数据 seed",
	RunE: func(cmd *cobra.Command, args []string) error {
		if strings.TrimSpace(metadataApplyFile) == "" {
			return fmt.Errorf("必须指定 --file")
		}
		seed, err := loadMetadataSeed(metadataApplyFile)
		if err != nil {
			return err
		}
		if err := validateReservedInternalSpaces(seed); err != nil {
			return err
		}
		calls, err := buildMetadataImportCalls(seed)
		if err != nil {
			return err
		}
		url := defaultMetadataImportURL(metadataApplyURL)
		if metadataApplyDryRun {
			return writeMetadataImportSummary(metadataImportSummary{Status: "dry_run", DryRun: true, MetadataURL: url, Planned: len(calls), Resources: countMetadataCalls(calls)})
		}
		summary, err := runMetadataApply(cmd.Context(), url, calls)
		if err != nil {
			return err
		}
		return writeMetadataImportSummary(summary)
	},
}

type metadataSeed struct {
	Spaces          []seedSpace          `yaml:"spaces"`
	DataSources     []seedDataSource     `yaml:"data_sources"`
	Subjects        []seedSubject        `yaml:"subjects"`
	SubjectSymbols  []seedSubjectSymbol  `yaml:"subject_symbols"`
	Datasets        []seedDataset        `yaml:"datasets"`
	DatasetSubjects []seedDatasetSubject `yaml:"dataset_subjects"`
	FieldGroups     []seedFieldGroup     `yaml:"field_groups"`
	Fields          []seedField          `yaml:"fields"`
	Factors         []seedFactor         `yaml:"factors"`
	DatasetColumns  []seedDatasetColumn  `yaml:"dataset_columns"`
	Views           []seedView           `yaml:"views"`
	ViewColumns     []seedViewColumn     `yaml:"view_columns"`
	Devices         []seedDevice         `yaml:"devices"`
}

type seedCommon struct {
	Status     string            `yaml:"status"`
	CreatedAt  string            `yaml:"created_at"`
	UpdatedAt  string            `yaml:"updated_at"`
	Attributes map[string]string `yaml:"attributes"`
}

type seedSpace struct {
	SpaceID     string `yaml:"space_id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Owner       string `yaml:"owner"`
	Market      string `yaml:"market"`
	Timezone    string `yaml:"timezone"`
	seedCommon  `yaml:",inline"`
}

type seedDataSource struct {
	SpaceID      string `yaml:"space_id"`
	DataSourceID string `yaml:"data_source_id"`
	Name         string `yaml:"name"`
	Kind         string `yaml:"kind"`
	Market       string `yaml:"market"`
	Timezone     string `yaml:"timezone"`
	ConfigJSON   string `yaml:"config_json"`
	seedCommon   `yaml:",inline"`
}

type seedSubject struct {
	SpaceID     string `yaml:"space_id"`
	SubjectID   string `yaml:"subject_id"`
	SubjectType string `yaml:"subject_type"`
	Name        string `yaml:"name"`
	Market      string `yaml:"market"`
	Currency    string `yaml:"currency"`
	Timezone    string `yaml:"timezone"`
	seedCommon  `yaml:",inline"`
}

type seedSubjectSymbol struct {
	SpaceID        string `yaml:"space_id"`
	SubjectID      string `yaml:"subject_id"`
	DataSourceID   string `yaml:"data_source_id"`
	ExternalSymbol string `yaml:"external_symbol"`
	seedCommon     `yaml:",inline"`
}

type seedDataset struct {
	SpaceID      string   `yaml:"space_id"`
	DatasetID    string   `yaml:"dataset_id"`
	DataSourceID string   `yaml:"data_source_id"`
	Name         string   `yaml:"name"`
	Description  string   `yaml:"description"`
	DataKind     string   `yaml:"data_kind"`
	DataNodeID   string   `yaml:"data_node_id"`
	KeepDuration string   `yaml:"keep_duration"`
	Freqs        []string `yaml:"freqs"`
	seedCommon   `yaml:",inline"`
}

type seedDatasetSubject struct {
	SpaceID            string `yaml:"space_id"`
	DatasetID          string `yaml:"dataset_id"`
	SubjectID          string `yaml:"subject_id"`
	SubjectRole        string `yaml:"subject_role"`
	EffectiveStartTime string `yaml:"effective_start_time"`
	EffectiveEndTime   string `yaml:"effective_end_time"`
	seedCommon         `yaml:",inline"`
}

type seedField struct {
	SpaceID            string `yaml:"space_id"`
	GroupID            string `yaml:"group_id"`
	FieldID            string `yaml:"field_id"`
	Name               string `yaml:"name"`
	Description        string `yaml:"description"`
	ValueType          string `yaml:"value_type"`
	Unit               string `yaml:"unit"`
	ValidationRuleJSON string `yaml:"validation_rule_json"`
	WriteExample       string `yaml:"write_example"`
	SortOrder          uint32 `yaml:"sort_order"`
	seedCommon         `yaml:",inline"`
}

type seedFieldGroup struct {
	SpaceID       string `yaml:"space_id"`
	GroupID       string `yaml:"group_id"`
	Name          string `yaml:"name"`
	Description   string `yaml:"description"`
	ParentGroupID string `yaml:"parent_group_id"`
	SortOrder     uint32 `yaml:"sort_order"`
	seedCommon    `yaml:",inline"`
}

type seedFactor struct {
	SpaceID     string `yaml:"space_id"`
	FactorID    string `yaml:"factor_id"`
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
	Algorithm   string `yaml:"algorithm"`
	ParamsJSON  string `yaml:"params_json"`
	ValueType   string `yaml:"value_type"`
	seedCommon  `yaml:",inline"`
}

type seedDatasetColumn struct {
	SpaceID    string   `yaml:"space_id"`
	DatasetID  string   `yaml:"dataset_id"`
	ColumnName string   `yaml:"column_name"`
	OriginType string   `yaml:"origin_type"`
	OriginID   string   `yaml:"origin_id"`
	ValueType  string   `yaml:"value_type"`
	Required   bool     `yaml:"required"`
	Aliases    []string `yaml:"aliases"`
	seedCommon `yaml:",inline"`
}

type seedView struct {
	SpaceID          string   `yaml:"space_id"`
	ViewID           string   `yaml:"view_id"`
	Name             string   `yaml:"name"`
	Description      string   `yaml:"description"`
	PrimaryDatasetID string   `yaml:"primary_dataset_id"`
	DatasetIDs       []string `yaml:"dataset_ids"`
	GrainKeys        []string `yaml:"grain_keys"`
	FilterJSON       string   `yaml:"filter_json"`
	Engine           string   `yaml:"engine"`
	KeepDuration     string   `yaml:"keep_duration"`
	seedCommon       `yaml:",inline"`
}

type seedViewColumn struct {
	SpaceID    string `yaml:"space_id"`
	ViewID     string `yaml:"view_id"`
	ColumnName string `yaml:"column_name"`
	OriginType string `yaml:"origin_type"`
	OriginID   string `yaml:"origin_id"`
	ValueType  string `yaml:"value_type"`
	OnlineTime string `yaml:"online_time"`
	SortOrder  uint32 `yaml:"sort_order"`
	seedCommon `yaml:",inline"`
}

type seedDevice struct {
	DeviceID   string `yaml:"device_id"`
	Name       string `yaml:"name"`
	Engine     string `yaml:"engine"`
	Endpoint   string `yaml:"endpoint"`
	ConfigJSON string `yaml:"config_json"`
	seedCommon `yaml:",inline"`
}

type metadataImportCall struct {
	Resource string
	Method   string
	Request  proto.Message
	Response proto.Message
	Exists   *metadataExistsProbe
}

type metadataExistsProbe struct {
	Method   string
	Request  proto.Message
	Response proto.Message
}

type metadataImportSummary struct {
	Status      string         `json:"status"`
	DryRun      bool           `json:"dry_run,omitempty"`
	MetadataURL string         `json:"metadata_url,omitempty"`
	Planned     int            `json:"planned"`
	Applied     int            `json:"applied"`
	Skipped     int            `json:"skipped"`
	Unchanged   int            `json:"unchanged,omitempty"`
	Resources   map[string]int `json:"resources"`
}
