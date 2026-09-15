package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
)

const (
	MergeModeSystem = "system"
	MergeModeCustom = "custom"

	UniverseSourceCollector  = "collector"
	UniverseSourceMembership = "membership"

	StorageResourcePending = "pending"
	StorageResourceCreated = "created"
	StorageResourceFailed  = "failed"
)

var publicRowKeys = map[string]struct{}{
	"subject_id": {}, "frequency": {}, "period_time": {}, "series_tag": {},
}

// KeyContract is the business row key shared by every source of an mdataset.
type KeyContract struct {
	SubjectID      string `json:"subject_id" yaml:"subject_id"`
	Frequency      string `json:"frequency" yaml:"frequency"`
	PeriodTime     string `json:"period_time" yaml:"period_time"`
	SeriesTag      string `json:"series_tag" yaml:"series_tag"`
	PeriodBoundary string `json:"period_boundary" yaml:"period_boundary"`
}

// SourceDatasetRef describes one required input Dataset.
type SourceDatasetRef struct {
	DatasetID      string   `json:"dataset_id" yaml:"dataset_id"`
	Frequency      string   `json:"frequency" yaml:"frequency"`
	PeriodBoundary string   `json:"period_boundary" yaml:"period_boundary"`
	Fields         []string `json:"fields" yaml:"fields"`
}

// FieldMapping is a deterministic source-field to mdataset-field mapping.
type FieldMapping struct {
	SourceDatasetID string `json:"source_dataset_id" yaml:"source_dataset_id"`
	SourceField     string `json:"source_field" yaml:"source_field"`
	TargetField     string `json:"target_field" yaml:"target_field"`
}

// MergedDataset is the control-plane definition of a composite factor Dataset.
type MergedDataset struct {
	DatasetID            string             `json:"dataset_id" gorm:"column:c_dataset_id;primaryKey"`
	SpaceID              string             `json:"space_id" gorm:"column:c_space_id"`
	Frequency            string             `json:"frequency" gorm:"column:c_frequency"`
	KeyContract          KeyContract        `json:"key_contract" gorm:"-"`
	KeyContractJSON      string             `json:"-" gorm:"column:c_key_contract_json"`
	ObjectSet            []string           `json:"object_set" gorm:"-"`
	ObjectSetJSON        string             `json:"-" gorm:"column:c_object_set_json"`
	Sources              []SourceDatasetRef `json:"sources" gorm:"-"`
	SourcesJSON          string             `json:"-" gorm:"column:c_sources_json"`
	MergeMode            string             `json:"merge_mode" gorm:"column:c_merge_mode"`
	UniverseSource       string             `json:"universe_source" gorm:"-"`
	FieldMappings        []FieldMapping     `json:"field_mappings" gorm:"-"`
	FieldMappingsJSON    string             `json:"-" gorm:"column:c_field_mappings_json"`
	Enabled              bool               `json:"enabled" gorm:"column:c_enabled"`
	ConfigSnapshotID     string             `json:"config_snapshot_id" gorm:"column:c_config_snapshot_id"`
	InputSemanticsHash   string             `json:"input_semantics_hash" gorm:"column:c_input_semantics_hash"`
	StorageResourceState string             `json:"storage_resource_state" gorm:"column:c_storage_resource_state"`
	StorageSchemaID      string             `json:"storage_schema_id" gorm:"column:c_storage_schema_id"`
}

func (MergedDataset) TableName() string { return "t_factor_merged_datasets" }

// MergedDatasetSnapshot is an immutable configuration snapshot.
type MergedDatasetSnapshot struct {
	SnapshotID         string `json:"snapshot_id" gorm:"column:c_snapshot_id;primaryKey"`
	DatasetID          string `json:"dataset_id" gorm:"column:c_dataset_id"`
	ConfigJSON         string `json:"config_json" gorm:"column:c_config_json"`
	InputSemanticsHash string `json:"input_semantics_hash" gorm:"column:c_input_semantics_hash"`
}

func (MergedDatasetSnapshot) TableName() string { return "t_factor_merged_dataset_snapshots" }

// MappedSourceField returns the mdataset field name for a source field.
// Public row keys are not prefixed.
func MappedSourceField(sourceDatasetID, field string) string {
	field = strings.TrimSpace(field)
	if _, ok := publicRowKeys[field]; ok {
		return field
	}
	return strings.TrimSpace(sourceDatasetID) + "__" + field
}

// CacheIdentity isolates caches by Dataset identity and Storage schema.
func CacheIdentity(datasetID, storageSchemaID string) string {
	return strings.TrimSpace(datasetID) + "\x00" + strings.TrimSpace(storageSchemaID)
}

// ValidateMergedDataset checks a new or disabled mdataset definition.
func ValidateMergedDataset(def MergedDataset) error {
	if strings.TrimSpace(def.DatasetID) == "" || strings.TrimSpace(def.SpaceID) == "" {
		return fmt.Errorf("dataset_id and space_id are required")
	}
	if strings.TrimSpace(def.Frequency) == "" {
		return fmt.Errorf("frequency is required")
	}
	if def.MergeMode != MergeModeSystem && def.MergeMode != MergeModeCustom {
		return fmt.Errorf("merge_mode must be system or custom")
	}
	if err := validateUniverseSource(def.UniverseSource); err != nil {
		return err
	}
	if err := validateKeyContract(def.KeyContract); err != nil {
		return err
	}
	if len(def.Sources) == 0 {
		return fmt.Errorf("source datasets are required")
	}
	seenSources := make(map[string]struct{}, len(def.Sources))
	for _, source := range def.Sources {
		id := strings.TrimSpace(source.DatasetID)
		if id == "" {
			return fmt.Errorf("source dataset_id is required")
		}
		if _, ok := seenSources[id]; ok {
			return fmt.Errorf("duplicate source dataset %q", id)
		}
		seenSources[id] = struct{}{}
		if strings.TrimSpace(source.Frequency) != strings.TrimSpace(def.Frequency) {
			return fmt.Errorf("source %q frequency %q does not match mdataset frequency", id, source.Frequency)
		}
		if strings.TrimSpace(source.PeriodBoundary) != strings.TrimSpace(def.KeyContract.PeriodBoundary) {
			return fmt.Errorf("source %q period boundary %q does not match mdataset period boundary", id, source.PeriodBoundary)
		}
		if len(source.Fields) == 0 {
			return fmt.Errorf("source %q fields are required", id)
		}
		for _, field := range source.Fields {
			if err := validateFieldName(field); err != nil {
				return fmt.Errorf("source %q: %w", id, err)
			}
		}
	}
	if len(def.FieldMappings) == 0 {
		return fmt.Errorf("field mappings are required")
	}
	targets := make(map[string]string, len(def.FieldMappings))
	for _, mapping := range def.FieldMappings {
		if _, ok := seenSources[strings.TrimSpace(mapping.SourceDatasetID)]; !ok {
			return fmt.Errorf("field mapping source %q is not a configured source", mapping.SourceDatasetID)
		}
		if err := validateFieldName(mapping.SourceField); err != nil {
			return err
		}
		if err := validateFieldName(mapping.TargetField); err != nil {
			return err
		}
		want := MappedSourceField(mapping.SourceDatasetID, mapping.SourceField)
		if mapping.TargetField != want && !isPublicRowKey(mapping.SourceField) {
			return fmt.Errorf("field mapping target %q must be %q", mapping.TargetField, want)
		}
		if existing, ok := targets[mapping.TargetField]; ok {
			return fmt.Errorf("field mapping target %q conflicts with %q", mapping.TargetField, existing)
		}
		targets[mapping.TargetField] = mapping.SourceDatasetID + "." + mapping.SourceField
	}
	return nil
}

// ValidateMergedDatasetUpdate rejects enabled in-place input-semantics changes.
func ValidateMergedDatasetUpdate(current, next MergedDataset) error {
	if current.Enabled && InputSemanticsHash(current) != InputSemanticsHash(next) {
		return fmt.Errorf("enabled mdataset %q cannot change input semantics", current.DatasetID)
	}
	return ValidateMergedDataset(next)
}

// InputSemanticsHash fingerprints the immutable input contract.
func InputSemanticsHash(def MergedDataset) string {
	type sourceWire struct {
		DatasetID      string   `json:"dataset_id"`
		Frequency      string   `json:"frequency"`
		PeriodBoundary string   `json:"period_boundary"`
		Fields         []string `json:"fields"`
	}
	type wire struct {
		Frequency   string         `json:"frequency"`
		KeyContract KeyContract    `json:"key_contract"`
		MergeMode   string         `json:"merge_mode"`
		Sources     []sourceWire   `json:"sources"`
		Mappings    []FieldMapping `json:"field_mappings"`
	}
	sources := append([]SourceDatasetRef(nil), def.Sources...)
	sort.Slice(sources, func(i, j int) bool { return sources[i].DatasetID < sources[j].DatasetID })
	payload := wire{Frequency: def.Frequency, KeyContract: def.KeyContract, MergeMode: def.MergeMode}
	for _, source := range sources {
		fields := append([]string(nil), source.Fields...)
		sort.Strings(fields)
		payload.Sources = append(payload.Sources, sourceWire{
			DatasetID: source.DatasetID, Frequency: source.Frequency, PeriodBoundary: source.PeriodBoundary, Fields: fields,
		})
	}
	mappings := append([]FieldMapping(nil), def.FieldMappings...)
	sort.Slice(mappings, func(i, j int) bool {
		if mappings[i].TargetField != mappings[j].TargetField {
			return mappings[i].TargetField < mappings[j].TargetField
		}
		return mappings[i].SourceDatasetID < mappings[j].SourceDatasetID
	})
	payload.Mappings = mappings
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func EncodeMergedDatasetConfig(def MergedDataset) (MergedDataset, error) {
	keyRaw, err := json.Marshal(def.KeyContract)
	if err != nil {
		return MergedDataset{}, err
	}
	objectRaw, err := json.Marshal(append([]string(nil), def.ObjectSet...))
	if err != nil {
		return MergedDataset{}, err
	}
	sourceRaw, err := json.Marshal(def.Sources)
	if err != nil {
		return MergedDataset{}, err
	}
	mappingRaw, err := json.Marshal(def.FieldMappings)
	if err != nil {
		return MergedDataset{}, err
	}
	def.KeyContractJSON = string(keyRaw)
	def.ObjectSetJSON = string(objectRaw)
	def.SourcesJSON = string(sourceRaw)
	def.FieldMappingsJSON = string(mappingRaw)
	def.InputSemanticsHash = InputSemanticsHash(def)
	if def.StorageResourceState == "" {
		def.StorageResourceState = StorageResourcePending
	}
	return def, nil
}

func DecodeMergedDataset(def MergedDataset) (MergedDataset, error) {
	if def.KeyContractJSON != "" {
		if err := json.Unmarshal([]byte(def.KeyContractJSON), &def.KeyContract); err != nil {
			return MergedDataset{}, fmt.Errorf("decode key contract: %w", err)
		}
	}
	if def.ObjectSetJSON != "" {
		if err := json.Unmarshal([]byte(def.ObjectSetJSON), &def.ObjectSet); err != nil {
			return MergedDataset{}, fmt.Errorf("decode object set: %w", err)
		}
	}
	if def.SourcesJSON != "" {
		if err := json.Unmarshal([]byte(def.SourcesJSON), &def.Sources); err != nil {
			return MergedDataset{}, fmt.Errorf("decode sources: %w", err)
		}
	}
	if def.FieldMappingsJSON != "" {
		if err := json.Unmarshal([]byte(def.FieldMappingsJSON), &def.FieldMappings); err != nil {
			return MergedDataset{}, fmt.Errorf("decode field mappings: %w", err)
		}
	}
	return def, nil
}

func validateUniverseSource(source string) error {
	switch strings.TrimSpace(source) {
	case "", UniverseSourceCollector, UniverseSourceMembership:
		return nil
	default:
		return fmt.Errorf("universe_source must be collector or membership")
	}
}

func validateKeyContract(contract KeyContract) error {
	for _, value := range []string{contract.SubjectID, contract.Frequency, contract.PeriodTime, contract.SeriesTag, contract.PeriodBoundary} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("key contract is incomplete")
		}
	}
	return nil
}

func validateFieldName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("field name is required")
	}
	if len(name) > 128 {
		return fmt.Errorf("field name %q exceeds 128 characters", name)
	}
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			continue
		}
		return fmt.Errorf("field name %q is invalid", name)
	}
	return nil
}

func isPublicRowKey(field string) bool {
	_, ok := publicRowKeys[strings.TrimSpace(field)]
	return ok
}

// PreferredMappedSource returns the first configured source Dataset, which is
// the default Python-facing short-name mapping for a merged Dataset.
func PreferredMappedSource(def MergedDataset) string {
	if len(def.Sources) == 0 {
		return ""
	}
	return strings.TrimSpace(def.Sources[0].DatasetID)
}

// ExpandPythonInputs maps algorithm short names onto physical mdataset fields.
// Qualified names are kept. When preferredSource is empty, a known mdataset
// falls back to its first Merge source.
func ExpandPythonInputs(datasetID, preferredSource string, requested []string) ([]string, error) {
	preferredSource = strings.TrimSpace(preferredSource)
	if preferredSource == "" {
		preferredSource = defaultPreferredMappedSource(datasetID)
	}
	out := make([]string, 0, len(requested))
	for _, req := range requested {
		req = strings.TrimSpace(req)
		if req == "" {
			return nil, fmt.Errorf("input column is required")
		}
		if strings.Contains(req, "__") || strings.Contains(req, ".") || preferredSource == "" {
			out = append(out, req)
			continue
		}
		out = append(out, MappedSourceField(preferredSource, req))
	}
	return out, nil
}

func defaultPreferredMappedSource(datasetID string) string {
	switch strings.TrimSpace(datasetID) {
	case "mdataset_binance_kline_1m":
		return "dataset_binance_spot_kline_1m"
	default:
		return ""
	}
}

// MapPythonInputColumns resolves requested Python column names against a
// physical schema. Exact names win; otherwise the first listed suffix match
// after "__" or "." is used so spot/swap dual fields stay deterministic.
func MapPythonInputColumns(requested, available []string) ([]string, error) {
	physical := make([]string, len(requested))
	for i, req := range requested {
		resolved, err := mapPythonInputColumn(strings.TrimSpace(req), available)
		if err != nil {
			return nil, err
		}
		physical[i] = resolved
	}
	return physical, nil
}

func mapPythonInputColumn(requested string, available []string) (string, error) {
	if requested == "" {
		return "", fmt.Errorf("input column is required")
	}
	for _, col := range available {
		if strings.TrimSpace(col) == requested {
			return requested, nil
		}
	}
	if strings.Contains(requested, ".") || strings.Contains(requested, "__") {
		return "", fmt.Errorf("%s", requested)
	}
	for _, col := range available {
		if columnFieldSuffix(strings.TrimSpace(col)) == requested {
			return strings.TrimSpace(col), nil
		}
	}
	return "", fmt.Errorf("%s", requested)
}

func columnFieldSuffix(name string) string {
	if i := strings.LastIndex(name, "__"); i >= 0 && i+2 < len(name) {
		return name[i+2:]
	}
	if i := strings.LastIndex(name, "."); i >= 0 && i+1 < len(name) {
		return name[i+1:]
	}
	return name
}
