package marketmanifest

type Manifest struct {
	SchemaVersion    int        `yaml:"schema_version"`
	MarketID         string     `yaml:"market_id"`
	SpaceID          string     `yaml:"space_id"`
	RegisterMetadata bool       `yaml:"register_metadata"`
	RuntimeEnabled   bool       `yaml:"runtime_enabled"`
	AssetClass       string     `yaml:"asset_class"`
	Timezone         string     `yaml:"timezone"`
	Exchange         Exchange   `yaml:"exchange"`
	ProductTypes     []string   `yaml:"product_types"`
	InstrumentTypes  []string   `yaml:"instrument_types"`
	Feeds            []Feed     `yaml:"feeds"`
	Providers        []Provider `yaml:"providers"`
	Datasets         []Dataset  `yaml:"datasets"`
	Execution        Execution  `yaml:"execution"`
	Readiness        Readiness  `yaml:"readiness"`
	Routing          Routing    `yaml:"routing"`
	Quality          Quality    `yaml:"quality"`
	Coverage         Coverage   `yaml:"coverage"`
	SCF              SCF        `yaml:"scf"`
}

type Exchange struct {
	ID   string `yaml:"id"`
	Name string `yaml:"name"`
}

type Feed struct {
	ID             string   `yaml:"id"`
	DatasetID      string   `yaml:"dataset_id"`
	Frequencies    []string `yaml:"frequencies"`
	VolumeUnit     string   `yaml:"volume_unit"`
	AmountUnit     string   `yaml:"amount_unit"`
	BucketAlign    string   `yaml:"bucket_alignment"`
	InstrumentType string   `yaml:"instrument_type"`
}

type Provider struct {
	ID            string   `yaml:"id"`
	Capabilities  []string `yaml:"capabilities"`
	CredentialEnv string   `yaml:"credential_env"`
	Endpoint      string   `yaml:"endpoint"`
	EndpointClass string   `yaml:"endpoint_class"`
	Quotas        []Quota  `yaml:"quotas"`
}

type Quota struct {
	Scope         string `yaml:"scope"`
	WindowSeconds int64  `yaml:"window_seconds"`
	Limit         int64  `yaml:"limit"`
	Weight        int64  `yaml:"weight"`
	ResetTimezone string `yaml:"reset_timezone"`
}

type Dataset struct {
	ID         string `yaml:"id"`
	Role       string `yaml:"role"`
	Feed       string `yaml:"feed"`
	ProviderID string `yaml:"provider_id"`
}

type Execution struct {
	TimeoutSeconds  int64 `yaml:"timeout_seconds"`
	JobBudgetMS     int64 `yaml:"job_budget_ms"`
	ReportReserveMS int64 `yaml:"report_reserve_ms"`
}

type Readiness struct {
	Status            string `yaml:"status"`
	Reason            string `yaml:"reason"`
	CapabilityEnabled bool   `yaml:"capability_enabled"`
}

type Routing struct {
	Policy string `yaml:"policy"`
}

type Quality struct {
	Policy string `yaml:"policy"`
}

type Coverage struct {
	Policy string `yaml:"policy"`
}

type SCF struct {
	FunctionName   string `yaml:"function_name"`
	TimeoutSeconds int64  `yaml:"timeout_seconds"`
}

type ProviderValidation struct {
	SchemaVersion       int             `yaml:"schema_version"`
	ProviderID          string          `yaml:"provider_id"`
	ProbedAt            string          `yaml:"probed_at"`
	ValidUntil          string          `yaml:"valid_until"`
	Environment         string          `yaml:"environment"`
	EndpointFingerprint string          `yaml:"endpoint_fingerprint"`
	EndpointClass       string          `yaml:"endpoint_class"`
	Frequencies         []string        `yaml:"frequencies"`
	Limits              Limits          `yaml:"limits"`
	QuotaScopes         []QuotaEvidence `yaml:"quota_scopes"`
	Network             NetworkEvidence `yaml:"network"`
	AdjustmentSemantics string          `yaml:"adjustment_semantics"`
	EvidenceSummary     string          `yaml:"evidence_summary"`
	GateIDs             []string        `yaml:"gate_ids"`
	CapabilityEnabled   bool            `yaml:"capability_enabled"`
}

type Limits struct {
	Batch int64 `yaml:"batch"`
	Point int64 `yaml:"point"`
}

type QuotaEvidence struct {
	Scope         string `yaml:"scope"`
	WindowSeconds int64  `yaml:"window_seconds"`
	Weight        int64  `yaml:"weight"`
	ResetTimezone string `yaml:"reset_timezone"`
}

type NetworkEvidence struct {
	Reachable bool   `yaml:"reachable"`
	Detail    string `yaml:"detail"`
}
