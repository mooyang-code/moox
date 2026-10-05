package enginehub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mooyang-code/moox/modules/factor/internal/domain"
)

// Snapshot assembles the enabled sets with their enabled member definitions
// (including source) and hashes them. When knownHash matches, notModified is
// true and sets is nil.
func (h *Hub) Snapshot(ctx context.Context, knownHash string) (string, bool, []domain.EngineSet, error) {
	all, err := h.store.ListSets(ctx)
	if err != nil {
		return "", false, nil, fmt.Errorf("list factor sets: %w", err)
	}
	sets := make([]domain.EngineSet, 0, len(all))
	for _, set := range all {
		if set.Status != domain.SetStatusEnabled {
			continue
		}
		members, err := h.store.ListMembers(ctx, set.SetID, domain.MemberStatusEnabled)
		if err != nil {
			return "", false, nil, fmt.Errorf("list enabled members of %s: %w", set.SetID, err)
		}
		factors := make([]domain.FactorDef, 0, len(members))
		for _, member := range members {
			factors = append(factors, member.Factor)
		}
		sort.Slice(factors, func(i, j int) bool { return factors[i].FactorID < factors[j].FactorID })
		sets = append(sets, domain.EngineSet{Set: set, Factors: factors, ResultReady: h.isReady(set.SetID)})
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].Set.SetID < sets[j].Set.SetID })
	hash, err := CatalogHash(sets)
	if err != nil {
		return "", false, nil, err
	}
	if knownHash != "" && knownHash == hash {
		return hash, true, nil, nil
	}
	return hash, false, sets, nil
}

type hashedSet struct {
	SetID           string         `json:"set_id"`
	SpaceID         string         `json:"space_id"`
	SourceDatasetID string         `json:"source_dataset_id"`
	Freq            string         `json:"freq"`
	SubjectMode     string         `json:"subject_mode"`
	Subjects        []string       `json:"subjects"`
	ResultDatasetID string         `json:"result_dataset_id"`
	Status          string         `json:"status"`
	ResultReady     bool           `json:"result_ready"`
	Factors         []hashedFactor `json:"factors"`
}

type hashedFactor struct {
	FactorID             string   `json:"factor_id"`
	Name                 string   `json:"name"`
	FactorType           string   `json:"factor_type"`
	SourceHash           string   `json:"source_hash"`
	InputColumns         []string `json:"input_columns"`
	Outputs              []string `json:"outputs"`
	ParamsJSON           string   `json:"params_json"`
	LookbackPeriods      int      `json:"lookback_periods"`
	AllowPartialUniverse bool     `json:"allow_partial_universe"`
}

// CatalogHash fingerprints everything the engine computes with. Timestamps are
// excluded so that a write without a semantic change does not force a resync;
// the source is covered by its content hash.
func CatalogHash(sets []domain.EngineSet) (string, error) {
	hashed := make([]hashedSet, 0, len(sets))
	for _, set := range sets {
		factors := make([]hashedFactor, 0, len(set.Factors))
		for _, factor := range set.Factors {
			factors = append(factors, hashedFactor{
				FactorID: factor.FactorID, Name: factor.Name, FactorType: factor.FactorType,
				SourceHash: factor.SourceHash, InputColumns: factor.InputColumns, Outputs: factor.Outputs,
				ParamsJSON: factor.ParamsJSON, LookbackPeriods: factor.LookbackPeriods,
				AllowPartialUniverse: factor.AllowPartialUniverse,
			})
		}
		hashed = append(hashed, hashedSet{
			SetID: set.Set.SetID, SpaceID: set.Set.SpaceID, SourceDatasetID: set.Set.SourceDatasetID,
			Freq: set.Set.Freq, SubjectMode: set.Set.SubjectMode, Subjects: set.Set.Subjects,
			ResultDatasetID: set.Set.ResultDatasetID, Status: set.Set.Status, ResultReady: set.ResultReady,
			Factors: factors,
		})
	}
	raw, err := json.Marshal(hashed)
	if err != nil {
		return "", fmt.Errorf("encode factor catalog: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
