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
func (h *Hub) Snapshot(ctx context.Context, knownHash string) (string, bool, []domain.EngineTask, error) {
	all, err := h.store.ListTasks(ctx)
	if err != nil {
		return "", false, nil, fmt.Errorf("list factor tasks: %w", err)
	}
	sets := make([]domain.EngineTask, 0, len(all))
	for _, set := range all {
		if set.Status != domain.TaskStatusEnabled {
			continue
		}
		members, err := h.store.ListMembers(ctx, set.TaskID, domain.MemberStatusEnabled)
		if err != nil {
			return "", false, nil, fmt.Errorf("list enabled members of %s: %w", set.TaskID, err)
		}
		defs := make([]domain.MemberDef, 0, len(members))
		for _, member := range members {
			defs = append(defs, member.Def())
		}
		sort.Slice(defs, func(i, j int) bool { return defs[i].Alias < defs[j].Alias })
		sets = append(sets, domain.EngineTask{Set: set, Members: defs, ResultReady: h.isReady(set.TaskID)})
	}
	sort.Slice(sets, func(i, j int) bool { return sets[i].Set.TaskID < sets[j].Set.TaskID })
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
	TaskID          string         `json:"task_id"`
	SpaceID         string         `json:"space_id"`
	SourceDatasetID string         `json:"source_dataset_id"`
	Freq            string         `json:"freq"`
	ResultDatasetID string         `json:"result_dataset_id"`
	OwnerInstanceID string         `json:"owner_instance_id"`
	Status          string         `json:"status"`
	ResultReady     bool           `json:"result_ready"`
	Members         []hashedMember `json:"members"`
}

type hashedMember struct {
	Alias                string `json:"alias"`
	FactorID             string `json:"factor_id"`
	CategoryID           string `json:"category_id"`
	Name                 string `json:"name"`
	SpecHash             string `json:"spec_hash"`
	DefinitionHash       string `json:"definition_hash"`
	AllowPartialUniverse bool   `json:"allow_partial_universe"`
	// Params 是成员的固定参数；同一别名删除重加、换参数后目录指纹必须变化，引擎才会重新同步。
	Params map[string]float64 `json:"params,omitempty"`
}

// CatalogHash fingerprints everything the engine computes with. Timestamps are
// excluded so that a write without a semantic change does not force a resync;
// the spec is covered by its content hash and the computation by definition_hash.
func CatalogHash(sets []domain.EngineTask) (string, error) {
	hashed := make([]hashedSet, 0, len(sets))
	for _, set := range sets {
		members := make([]hashedMember, 0, len(set.Members))
		for _, member := range set.Members {
			factor := member.Factor
			members = append(members, hashedMember{
				Alias: member.Alias, FactorID: factor.FactorID, CategoryID: factor.CategoryID, Name: factor.Name, SpecHash: factor.SpecHash,
				DefinitionHash: domain.DefinitionHash(factor), AllowPartialUniverse: factor.AllowPartialUniverse,
				Params: member.Params,
			})
		}
		hashed = append(hashed, hashedSet{
			TaskID: set.Set.TaskID, SpaceID: set.Set.SpaceID, SourceDatasetID: set.Set.SourceDatasetID,
			Freq:            set.Set.Freq,
			ResultDatasetID: set.Set.ResultDatasetID, OwnerInstanceID: set.Set.OwnerInstanceID, Status: set.Set.Status,
			ResultReady: set.ResultReady, Members: members,
		})
	}
	raw, err := json.Marshal(hashed)
	if err != nil {
		return "", fmt.Errorf("encode factor catalog: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
