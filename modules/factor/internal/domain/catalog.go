package domain

// CatalogSnapshot is a complete, revisioned definition and binding catalog.
// Runtime state and output manifests are deliberately not part of this contract.
type CatalogSnapshot struct {
	Revision int64           `json:"revision"`
	Factors  []FactorDef     `json:"factors"`
	Bindings []FactorBinding `json:"bindings"`
}
