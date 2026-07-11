package storageio

import "github.com/mooyang-code/moox/modules/collector/internal/marketdata"

type DatasetRole string

const (
	RoleProviderData  DatasetRole = "provider_data"
	RoleUnifiedData   DatasetRole = "unified_data"
	RoleQualityEvent  DatasetRole = "quality_event"
	RoleCoverageState DatasetRole = "coverage_state"
)

type Binding struct {
	SpaceID        string
	DatasetID      string
	DataSourceID   string
	Role           DatasetRole
	Feed           string
	ProviderID     marketdata.ProviderID
	ProductType    marketdata.ProductType
	InstrumentType marketdata.InstrumentType
	RequiredVolume bool
	RequiredAmount bool
	VolumeUnit     string
	AmountUnit     string
	SchemaVersion  int64
}
