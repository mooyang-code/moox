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

// Snapshot 组装已启用任务快照、库中当前存在的全部任务 ID（含停用与待创建）以及目录指纹。
// knownHash 一致时 notModified 为 true，sets 与 existingTaskIDs 均为空。
func (h *Hub) Snapshot(ctx context.Context, knownHash string) (string, bool, []domain.EngineTask, []string, error) {
	all, err := h.store.ListTasks(ctx)
	if err != nil {
		return "", false, nil, nil, fmt.Errorf("list factor tasks: %w", err)
	}
	existing := make([]string, 0, len(all))
	sets := make([]domain.EngineTask, 0, len(all))
	for _, set := range all {
		existing = append(existing, set.TaskID)
		if set.Status != domain.TaskStatusEnabled {
			continue
		}
		members, err := h.store.ListMembers(ctx, set.TaskID, domain.MemberStatusEnabled)
		if err != nil {
			return "", false, nil, nil, fmt.Errorf("list enabled members of %s: %w", set.TaskID, err)
		}
		defs := make([]domain.MemberDef, 0, len(members))
		for _, member := range members {
			defs = append(defs, member.Def())
		}
		sort.Slice(defs, func(i, j int) bool { return defs[i].Key < defs[j].Key })
		sets = append(sets, domain.EngineTask{Set: set, Members: defs, ResultReady: h.isReady(set.TaskID), SourceSeeding: h.sourceSeeding(set)})
	}
	sort.Strings(existing)
	sort.Slice(sets, func(i, j int) bool { return sets[i].Set.TaskID < sets[j].Set.TaskID })
	hash, err := CatalogHash(sets, existing)
	if err != nil {
		return "", false, nil, nil, err
	}
	if knownHash != "" && knownHash == hash {
		return hash, true, nil, nil, nil
	}
	return hash, false, sets, existing, nil
}

type hashedSet struct {
	TaskID          string         `json:"task_id"`
	SpaceID         string         `json:"space_id"`
	SourceDatasetID string         `json:"source_dataset_id"`
	Freq            string         `json:"freq"`
	ResultDatasetID string         `json:"result_dataset_id"`
	Status          string         `json:"status"`
	ResultReady     bool           `json:"result_ready"`
	SourceSeeding   bool           `json:"source_seeding"`
	Members         []hashedMember `json:"members"`
}

type hashedMember struct {
	Key                  string `json:"key"`
	Kind                 string `json:"kind"`
	FactorID             string `json:"factor_id"`
	CategoryID           string `json:"category_id"`
	Name                 string `json:"name"`
	SpecHash             string `json:"spec_hash"`
	DefinitionHash       string `json:"definition_hash"`
	AllowPartialUniverse bool   `json:"allow_partial_universe"`
	// Params 是成员的固定参数，或可变成员的默认参数；同一成员键删除重加、换参数后目录指纹必须变化，引擎才会重新同步。
	Params map[string]float64 `json:"params,omitempty"`
	// UDFs 是定义引用的自定义算子版本（名称@哈希）：definition_hash 已经随版本变化，这里再显式列出，
	// 让“只切换了版本”在指纹里一目了然，也覆盖编译失败时（definition_hash 取备用值）的变化。
	UDFs []string `json:"udfs,omitempty"`
}

// CatalogHash 把已启用任务与管理端库中当前存在的全部任务 ID 一并计入目录指纹（忽略时间戳，避免无语义写入触发重同步）：
// 即使任务先停用再删除（启用任务集合未变），existingTaskIDs 缩短也会改变目录指纹，
// 使引擎在下一次同步时及时清理已删除任务的本地 watermark 与 gapledger。
func CatalogHash(sets []domain.EngineTask, existingTaskIDs []string) (string, error) {
	hashed := make([]hashedSet, 0, len(sets))
	for _, set := range sets {
		members := make([]hashedMember, 0, len(set.Members))
		for _, member := range set.Members {
			factor := member.Factor
			members = append(members, hashedMember{
				Key: member.Key, Kind: member.Kind, FactorID: factor.FactorID, CategoryID: factor.CategoryID, Name: factor.Name, SpecHash: factor.SpecHash,
				DefinitionHash: domain.DefinitionHash(factor), AllowPartialUniverse: factor.AllowPartialUniverse,
				Params: member.Params, UDFs: udfRefs(factor),
			})
		}
		hashed = append(hashed, hashedSet{
			TaskID: set.Set.TaskID, SpaceID: set.Set.SpaceID, SourceDatasetID: set.Set.SourceDatasetID,
			Freq:            set.Set.Freq,
			ResultDatasetID: set.Set.ResultDatasetID, Status: set.Set.Status,
			ResultReady: set.ResultReady, SourceSeeding: set.SourceSeeding, Members: members,
		})
	}
	existing := append([]string(nil), existingTaskIDs...)
	sort.Strings(existing)
	payload := struct {
		Sets            []hashedSet `json:"sets"`
		ExistingTaskIDs []string    `json:"existing_task_ids,omitempty"`
	}{Sets: hashed, ExistingTaskIDs: existing}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode factor catalog: %w", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

// udfRefs 返回定义的自定义算子绑定（名称@哈希，按名称排序）。
func udfRefs(def domain.FactorDef) []string {
	if len(def.UDFs) == 0 {
		return nil
	}
	refs := make([]string, 0, len(def.UDFs))
	for name, udf := range def.UDFs {
		refs = append(refs, name+"@"+udf.Hash)
	}
	sort.Strings(refs)
	return refs
}
