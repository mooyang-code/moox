package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	adminclient "github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"gopkg.in/yaml.v3"
)

var factorIDPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// setupFactorTask 是配置里的一个因子任务及其成员。
type setupFactorTask struct {
	TaskID          string
	Name            string
	SpaceID         string
	SourceDatasetID string
	Freq            string
	Members         []setupFactorMember
}

// setupFactorDefinition is the global factor contract read from a YAML factor
// library file. It carries no set or status: those belong to a membership.
type setupFactorDefinition struct {
	FactorID             string
	Name                 string
	Spec                 string
	AllowPartialUniverse bool
	// CategoryID 是因子分类（空为未分类）；CategoryName 非空时初始化会建立或更新这个分类。
	CategoryID   string
	CategoryName string
}

// libraryEntry 是 YAML 因子库文件里的一项：[{factor_id, name, category_id, category_name, spec, allow_partial_universe}]。
type libraryEntry struct {
	FactorID             string `yaml:"factor_id"`
	Name                 string `yaml:"name"`
	CategoryID           string `yaml:"category_id"`
	CategoryName         string `yaml:"category_name"`
	Spec                 string `yaml:"spec"`
	AllowPartialUniverse bool   `yaml:"allow_partial_universe"`
}

// readFactorLibrary 严格解析一个因子库文件：未知字段、多文档、缺少 factor_id/name/spec、重复 factor_id 都报错。
func readFactorLibrary(path string) (map[string]libraryEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	var entries []libraryEntry
	if err := decoder.Decode(&entries); err != nil {
		return nil, fmt.Errorf("parse factor library %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("factor library %s must contain exactly one YAML document", path)
	}
	library := make(map[string]libraryEntry, len(entries))
	for index, entry := range entries {
		entry.FactorID, entry.Name = strings.TrimSpace(entry.FactorID), strings.TrimSpace(entry.Name)
		entry.CategoryID, entry.CategoryName = strings.TrimSpace(entry.CategoryID), strings.TrimSpace(entry.CategoryName)
		if entry.CategoryName != "" && entry.CategoryID == "" {
			return nil, fmt.Errorf("factor library %s item %d sets category_name without category_id", path, index+1)
		}
		if entry.FactorID == "" || entry.Name == "" || strings.TrimSpace(entry.Spec) == "" {
			return nil, fmt.Errorf("factor library %s item %d requires factor_id, name and spec", path, index+1)
		}
		if _, duplicate := library[entry.FactorID]; duplicate {
			return nil, fmt.Errorf("factor library %s defines %q more than once", path, entry.FactorID)
		}
		library[entry.FactorID] = entry
	}
	return library, nil
}

// setupFactorMember 是任务里的一个成员。Kind 是 fixed 或 var；Params 是固定成员的固定参数、或可变成员的默认参数
// （没给的取定义的默认值）。成员键由服务端推导。
type setupFactorMember struct {
	FactorID string
	Kind     string
	Params   map[string]float64
	Status   string
}

// setupFactorPlan 是一次初始化要应用的内容：先分类与定义，再任务，最后任务里的成员。
type setupFactorPlan struct {
	Tasks       []setupFactorTask
	Definitions []setupFactorDefinition
	// Warnings 是不阻止初始化但用户应该知道的配置关联问题（例如交易时段市场的保留期估算装不下因子回看根数）。
	Warnings []string
}

func (p setupFactorPlan) empty() bool {
	return len(p.Tasks) == 0 && len(p.Definitions) == 0
}

func (p setupFactorPlan) memberCount() int {
	count := 0
	for _, task := range p.Tasks {
		count += len(task.Members)
	}
	return count
}

// 逐项结果。
const (
	itemExists   = "exists"   // 已存在且一致
	itemCreated  = "created"  // 新建
	itemConflict = "conflict" // 与已有的不一致，需要人工处理
	itemOrphan   = "orphan"   // 任务里有、配置里已去掉的成员：只报告，不移除
)

// setupFactorItem 是一项的处理结果：任务、定义或成员。成员项带成员键与全部列名。
type setupFactorItem struct {
	Kind      string   `json:"kind"`
	ID        string   `json:"id"`
	Result    string   `json:"result"`
	Detail    string   `json:"detail,omitempty"`
	MemberKey string   `json:"member_key,omitempty"`
	Columns   []string `json:"columns,omitempty"`
}

type setupFactorSummary struct {
	Enabled          bool `json:"enabled"`
	Definitions      int  `json:"definitions"`
	Tasks            int  `json:"tasks"`
	Members          int  `json:"members"`
	TasksCreated     int  `json:"tasks_created"`
	Imported         int  `json:"imported"`
	Unchanged        int  `json:"unchanged"`
	MembersAdded     int  `json:"members_added"`
	MembersEnabled   int  `json:"members_enabled"`
	MembersUnchanged int  `json:"members_unchanged"`
	Conflicts        int  `json:"conflicts"`
	// Items 逐项列出已存在 / 新建 / 冲突 / 配置里已去掉的成员，以及每个成员的成员键与列名。
	Items []setupFactorItem `json:"items,omitempty"`
	// Warnings 是配置关联的提醒（例如保留期与因子回看根数），不阻止初始化。
	Warnings []string `json:"warnings,omitempty"`
}

type setupInitFactor interface {
	Apply(context.Context, setupFactorPlan) (setupFactorSummary, error)
	Close() error
}

func loadSetupFactors(manifest setupconfig.Manifest, repoRoot string) (setupFactorPlan, error) {
	var plan setupFactorPlan
	if !manifest.Factors.Enabled {
		return plan, nil
	}
	if strings.TrimSpace(manifest.Factors.SourceDir) == "" {
		manifest.Factors.SourceDir = "modules/factor/factors"
	}
	definitions, tasks := manifest.Factors.Definitions, manifest.Factors.Tasks
	// 因子任务指定具体的源数据集，所以启用因子时必须显式声明任务和定义。
	if len(definitions) == 0 && len(tasks) == 0 {
		return plan, fmt.Errorf("factors.enabled requires [[factors.tasks]] and [[factors.definitions]]")
	}
	if len(definitions) == 0 {
		return plan, fmt.Errorf("factors.definitions must be configured when factors.tasks is specified")
	}
	root := filepath.Join(repoRoot, filepath.FromSlash(manifest.Factors.SourceDir))
	seen := make(map[string]struct{}, len(definitions))
	libraries := map[string]map[string]libraryEntry{}
	for index, item := range definitions {
		factorID := strings.TrimSpace(item.FactorID)
		if !factorIDPattern.MatchString(factorID) {
			return plan, fmt.Errorf("factors.definitions[%d].factor_id is invalid", index)
		}
		if _, exists := seen[factorID]; exists {
			return plan, fmt.Errorf("factor %q is configured more than once", factorID)
		}
		seen[factorID] = struct{}{}
		path := filepath.Join(root, filepath.FromSlash(item.File))
		resolvedRoot, err := filepath.Abs(root)
		if err != nil {
			return plan, fmt.Errorf("resolve factors directory: %w", err)
		}
		resolvedRoot, err = filepath.EvalSymlinks(resolvedRoot)
		if err != nil {
			return plan, fmt.Errorf("resolve factors directory: %w", err)
		}
		resolvedPath, err := filepath.Abs(path)
		if err != nil {
			return plan, fmt.Errorf("resolve factor %q path: %w", factorID, err)
		}
		resolvedPath, err = filepath.EvalSymlinks(resolvedPath)
		if err != nil || !pathWithin(resolvedRoot, resolvedPath) {
			return plan, fmt.Errorf("factor %q file must stay under factors.source_dir", factorID)
		}
		library, loaded := libraries[resolvedPath]
		if !loaded {
			if library, err = readFactorLibrary(resolvedPath); err != nil {
				return plan, fmt.Errorf("read factor %q library: %w", factorID, err)
			}
			libraries[resolvedPath] = library
		}
		entry, ok := library[factorID]
		if !ok {
			return plan, fmt.Errorf("factor library %s has no factor %q", item.File, factorID)
		}
		plan.Definitions = append(plan.Definitions, setupFactorDefinition{
			FactorID: factorID, Name: entry.Name, Spec: entry.Spec, AllowPartialUniverse: entry.AllowPartialUniverse,
			CategoryID: entry.CategoryID, CategoryName: entry.CategoryName,
		})
	}
	sort.Slice(plan.Definitions, func(i, j int) bool { return plan.Definitions[i].FactorID < plan.Definitions[j].FactorID })
	for _, task := range tasks {
		planned := setupFactorTask{TaskID: task.ID, Name: task.Name, SpaceID: task.SpaceID, SourceDatasetID: task.SourceDatasetID, Freq: task.Freq}
		for _, member := range task.Members {
			kind := "fixed"
			if member.Var {
				kind = "var"
			}
			planned.Members = append(planned.Members, setupFactorMember{FactorID: member.Factor, Kind: kind, Params: member.Params, Status: defaultString(member.Status, "enabled")})
		}
		plan.Tasks = append(plan.Tasks, planned)
	}
	sort.Slice(plan.Tasks, func(i, j int) bool { return plan.Tasks[i].TaskID < plan.Tasks[j].TaskID })
	// 保留期必须大于因子的最大回看根数：两处配置是关联的，初始化时核对。
	errs, warnings := factorRetentionFindings(manifest.StoragePolicy(), plan)
	if len(errs) > 0 {
		return plan, fmt.Errorf("config_invalid: %s", joinFindings(errs))
	}
	plan.Warnings = warnings
	return plan, nil
}

func defaultString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

type remoteSetupFactor struct {
	gateway commandGateway
	client  factorJSONClient
	// out 接收逐项结果（已存在 / 新建 / 冲突）；为空时写 stderr。
	out io.Writer
}

type factorJSONClient interface {
	CallGatewayJSON(context.Context, string, string, any, any) error
}

func defaultOpenSetupFactor(ctx context.Context, snapshot *setupconfig.Snapshot) (setupInitFactor, error) {
	gateway, err := openCommandGateway(ctx, "", snapshot)
	if err != nil {
		return nil, err
	}
	return &remoteSetupFactor{gateway: gateway, client: &adminclient.Client{Gateway: gateway}}, nil
}

type factorAPIRetInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// factorAPIDef mirrors the FactorDef proto JSON: a definition has no task or status.
type factorAPIDef struct {
	FactorID             string `json:"factor_id"`
	Name                 string `json:"name"`
	CategoryID           string `json:"category_id"`
	AllowPartialUniverse bool   `json:"allow_partial_universe"`
	DefinitionHash       string `json:"definition_hash"`
	// Params 是定义声明的可调参数；默认值用来补全成员的参数，判断配置里的成员是否已经存在。
	Params []factorAPIParam `json:"params"`
}

// factorAPIParam mirrors the FactorParamDecl proto JSON (only what the CLI reads).
type factorAPIParam struct {
	Name         string  `json:"name"`
	DefaultValue float64 `json:"default_value"`
}

// factorAPIMember mirrors the FactorMember proto JSON.
type factorAPIMember struct {
	TaskID        string             `json:"task_id"`
	FactorID      string             `json:"factor_id"`
	MemberKey     string             `json:"member_key"`
	Kind          string             `json:"kind"`
	Status        string             `json:"status"`
	Params        map[string]float64 `json:"params"`
	OutputColumns []string           `json:"output_columns"`
}

type factorAPIResponse struct {
	RetInfo *factorAPIRetInfo `json:"ret_info"`
	// ValidateFactorSpec 的结果。
	Valid          bool   `json:"valid"`
	DefinitionHash string `json:"definition_hash"`
	Diagnostic     struct {
		Message string `json:"message"`
	} `json:"diagnostic"`
	Factor     factorAPIDef `json:"factor"`
	FactorTask struct {
		TaskID          string `json:"task_id"`
		Name            string `json:"name"`
		SpaceID         string `json:"space_id"`
		SourceDatasetID string `json:"source_dataset_id"`
		Freq            string `json:"freq"`
		Status          string `json:"status"`
		BoundInstanceID string `json:"bound_instance_id"`
	} `json:"factor_task"`
	// Members 是 GetFactorTask 返回的任务成员；Member 是 AddTaskMember、SetTaskMemberStatus 返回的成员。
	Members []factorAPIMember `json:"members"`
	Member  factorAPIMember   `json:"member"`
}

func (r factorAPIResponse) err(method string) error {
	if r.RetInfo == nil {
		return fmt.Errorf("FactorMgr %s failed: missing ret_info", method)
	}
	if r.RetInfo.Code == 0 || r.RetInfo.Code == 200 {
		return nil
	}
	return fmt.Errorf("FactorMgr %s failed (%d): %s", method, r.RetInfo.Code, r.RetInfo.Msg)
}

func (r *remoteSetupFactor) call(ctx context.Context, method string, body any, response *factorAPIResponse) error {
	if r == nil || r.client == nil {
		return fmt.Errorf("FactorMgr requires the operator SSH gateway")
	}
	if err := r.client.CallGatewayJSON(ctx, "trpc.moox.factor.FactorMgr", method, body, response); err != nil {
		return err
	}
	return response.err(method)
}

// Apply 幂等且只增不减：缺的就建，已有的核对一致，配置里没有的定义和成员不删除（成员只报告）。
// 逐项记录结果；有冲突时仍处理完其他项，最后返回错误，摘要里带全部条目。
func (r *remoteSetupFactor) Apply(ctx context.Context, plan setupFactorPlan) (setupFactorSummary, error) {
	summary := setupFactorSummary{Enabled: !plan.empty(), Definitions: len(plan.Definitions), Tasks: len(plan.Tasks), Members: plan.memberCount()}
	record := func(item setupFactorItem) {
		summary.Items = append(summary.Items, item)
		if item.Result == itemConflict {
			summary.Conflicts++
		}
		r.report(item)
	}
	// 分类先于定义：定义引用的分类必须存在。同一分类在多个定义上出现时名称以第一次为准。
	categories := map[string]string{}
	for _, def := range plan.Definitions {
		if def.CategoryName == "" {
			continue
		}
		if _, done := categories[def.CategoryID]; done {
			continue
		}
		categories[def.CategoryID] = def.CategoryName
		upserted := factorAPIResponse{}
		if err := r.call(ctx, "UpsertFactorCategory", map[string]any{"category": map[string]any{
			"category_id": def.CategoryID, "name": def.CategoryName,
		}}, &upserted); err != nil {
			return summary, err
		}
	}
	// defaults 是各定义声明的参数默认值，用来把配置里给出的参数补全成完整参数，再与现有成员的参数比较；
	// brokenDefs 是与已有定义冲突的因子：引用它们的成员本次不处理。
	defaults := make(map[string]map[string]float64, len(plan.Definitions))
	brokenDefs := map[string]bool{}
	for _, def := range plan.Definitions {
		get := factorAPIResponse{}
		err := r.call(ctx, "GetFactor", map[string]any{"factor_id": def.FactorID}, &get)
		if err != nil && !factorAPINotFound(get) {
			return summary, err
		}
		if err != nil {
			create := factorAPIResponse{}
			if createErr := r.call(ctx, "CreateFactor", map[string]any{"factor": map[string]any{
				"factor_id": def.FactorID, "name": def.Name, "spec": def.Spec,
				"allow_partial_universe": def.AllowPartialUniverse, "category_id": def.CategoryID,
			}}, &create); createErr != nil {
				return summary, createErr
			}
			defaults[def.FactorID] = paramDefaults(create.Factor)
			summary.Imported++
			record(setupFactorItem{Kind: "definition", ID: def.FactorID, Result: itemCreated})
			continue
		}
		// 幂等比较用 definition_hash：本地定义的哈希由 FactorMgr 编译得到，只改注释、格式不算不同。
		validated := factorAPIResponse{}
		if err := r.call(ctx, "ValidateFactorSpec", map[string]any{
			"spec": def.Spec, "allow_partial_universe": def.AllowPartialUniverse,
		}, &validated); err != nil {
			return summary, err
		}
		if !validated.Valid {
			return summary, fmt.Errorf("factor %q spec is invalid: %s", def.FactorID, validated.Diagnostic.Message)
		}
		if get.Factor.Name != def.Name || get.Factor.DefinitionHash != validated.DefinitionHash {
			brokenDefs[def.FactorID] = true
			record(setupFactorItem{Kind: "definition", ID: def.FactorID, Result: itemConflict, Detail: "已存在但定义不同；请更新 moox.toml 或显式替换已有定义"})
			continue
		}
		// 分类只是元数据（不进 definition_hash），不同时直接改成因子库里的分类。
		if def.CategoryID != "" && get.Factor.CategoryID != def.CategoryID {
			moved := factorAPIResponse{}
			if err := r.call(ctx, "SetFactorCategory", map[string]any{"factor_id": def.FactorID, "category_id": def.CategoryID}, &moved); err != nil {
				return summary, err
			}
		}
		defaults[def.FactorID] = paramDefaults(get.Factor)
		summary.Unchanged++
		record(setupFactorItem{Kind: "definition", ID: def.FactorID, Result: itemExists})
	}
	for _, task := range plan.Tasks {
		existing, ok, err := r.applyTask(ctx, task, &summary, record)
		if err != nil {
			return summary, err
		}
		if !ok {
			continue
		}
		if err := r.applyMembers(ctx, task, existing, defaults, brokenDefs, &summary, record); err != nil {
			return summary, err
		}
	}
	if summary.Conflicts > 0 {
		return summary, fmt.Errorf("因子配置有 %d 项与已有内容冲突，见摘要 items 里 result=conflict 的条目", summary.Conflicts)
	}
	return summary, nil
}

// applyTask 找到或建出任务，返回它现有的成员；身份与配置不一致时记冲突并返回 ok=false。
func (r *remoteSetupFactor) applyTask(ctx context.Context, task setupFactorTask, summary *setupFactorSummary, record func(setupFactorItem)) ([]factorAPIMember, bool, error) {
	get := factorAPIResponse{}
	err := r.call(ctx, "GetFactorTask", map[string]any{"task_id": task.TaskID}, &get)
	if err != nil && !factorAPINotFound(get) {
		return nil, false, err
	}
	createTask := func() error {
		create := factorAPIResponse{}
		return r.call(ctx, "CreateFactorTask", map[string]any{"factor_task": map[string]any{
			"task_id": task.TaskID, "name": task.Name, "space_id": task.SpaceID, "source_dataset_id": task.SourceDatasetID,
			"freq": task.Freq, "status": "pending",
		}}, &create)
	}
	if err != nil {
		if createErr := createTask(); createErr != nil {
			return nil, false, createErr
		}
		summary.TasksCreated++
		record(setupFactorItem{Kind: "task", ID: task.TaskID, Result: itemCreated, Detail: task.Name})
		return nil, true, nil
	}
	got := get.FactorTask
	if got.SpaceID != task.SpaceID || got.SourceDatasetID != task.SourceDatasetID || got.Freq != task.Freq || got.Name != task.Name {
		record(setupFactorItem{Kind: "task", ID: task.TaskID, Result: itemConflict,
			Detail: fmt.Sprintf("已存在但身份不同（空间 %s、源数据集 %s、频率 %s、名称 %s）；任务身份创建后不能改", got.SpaceID, got.SourceDatasetID, got.Freq, got.Name)})
		return nil, false, nil
	}
	detail := ""
	if got.Status == "pending" {
		// 创建被中断，任务停在 pending；CreateFactorTask 会接着建结果数据集。
		if createErr := createTask(); createErr != nil {
			return nil, false, createErr
		}
		detail = "上次创建被中断，已接着完成"
	}
	record(setupFactorItem{Kind: "task", ID: task.TaskID, Result: itemExists, Detail: detail})
	return get.Members, true, nil
}

// applyMembers 把任务的成员对齐到配置：成员的身份是成员键，由服务端按（因子、类型、参数）推导；
// 这里用「同一因子 + 同一类型 + 同样的完整参数」找现有成员，找不到再加。配置里已去掉的现有成员只报告。
func (r *remoteSetupFactor) applyMembers(ctx context.Context, task setupFactorTask, existing []factorAPIMember, defaults map[string]map[string]float64, brokenDefs map[string]bool, summary *setupFactorSummary, record func(setupFactorItem)) error {
	matched := map[string]bool{}
	for _, planned := range task.Members {
		label := task.TaskID + "/" + planned.FactorID
		if brokenDefs[planned.FactorID] {
			record(setupFactorItem{Kind: "member", ID: label, Result: itemConflict, Detail: "因子定义与已有的不同，本次不处理这个成员"})
			continue
		}
		decls, known := defaults[planned.FactorID]
		if !known {
			return fmt.Errorf("member references factor %q that is not defined", planned.FactorID)
		}
		want := completeParams(decls, planned.Params)
		var found *factorAPIMember
		var sameFactorVar *factorAPIMember
		for index := range existing {
			member := &existing[index]
			if member.FactorID != planned.FactorID || member.Kind != planned.Kind {
				continue
			}
			if planned.Kind == "var" {
				sameFactorVar = member
			}
			if sameParamValues(member.Params, want) {
				found = member
				break
			}
		}
		if found == nil && sameFactorVar != nil {
			record(setupFactorItem{Kind: "member", ID: label, Result: itemConflict, MemberKey: sameFactorVar.MemberKey,
				Detail: "可变成员已存在但默认参数不同；参数加成员之后不能改，需要的话先移除成员再重新加入"})
			matched[sameFactorVar.MemberKey] = true
			continue
		}
		result := itemExists
		if found == nil {
			body := map[string]any{"task_id": task.TaskID, "factor_id": planned.FactorID, "kind": planned.Kind}
			if len(planned.Params) > 0 {
				body["params"] = planned.Params
			}
			added := factorAPIResponse{}
			if err := r.call(ctx, "AddTaskMember", body, &added); err != nil {
				return err
			}
			if added.Member.MemberKey == "" {
				return fmt.Errorf("AddTaskMember returned no member key for factor %q in task %q", planned.FactorID, task.TaskID)
			}
			member := added.Member
			if member.Status == "" {
				member.Status = "disabled"
			}
			existing = append(existing, member)
			found = &existing[len(existing)-1]
			summary.MembersAdded++
			result = itemCreated
		}
		matched[found.MemberKey] = true
		detail := ""
		if found.Status != planned.Status {
			status := factorAPIResponse{}
			if err := r.call(ctx, "SetTaskMemberStatus", map[string]any{
				"task_id": task.TaskID, "member_key": found.MemberKey, "status": planned.Status,
			}, &status); err != nil {
				return err
			}
			found.Status = planned.Status
			summary.MembersEnabled++
			detail = "状态改为 " + planned.Status
		} else {
			summary.MembersUnchanged++
		}
		record(setupFactorItem{Kind: "member", ID: label, Result: result, MemberKey: found.MemberKey, Columns: found.OutputColumns, Detail: detail})
	}
	for _, member := range existing {
		if !matched[member.MemberKey] {
			record(setupFactorItem{Kind: "member", ID: task.TaskID + "/" + member.FactorID, Result: itemOrphan, MemberKey: member.MemberKey,
				Detail: "任务里有、配置里已去掉；只报告，不移除"})
		}
	}
	return nil
}

// report 把一项的结果逐行打到 out（默认 stderr）：已存在 / 新建 / 冲突，成员项带成员键与全部列名。
func (r *remoteSetupFactor) report(item setupFactorItem) {
	out := r.out
	if out == nil {
		out = os.Stderr
	}
	label := map[string]string{itemExists: "已存在", itemCreated: "新建", itemConflict: "冲突", itemOrphan: "配置已去掉"}[item.Result]
	line := fmt.Sprintf("因子%s %s：%s", map[string]string{"task": "任务", "definition": "定义", "member": "成员"}[item.Kind], item.ID, label)
	if item.MemberKey != "" {
		line += "，成员键 " + item.MemberKey
	}
	if len(item.Columns) > 0 {
		line += "，列 " + strings.Join(item.Columns, "、")
	}
	if item.Detail != "" {
		line += "（" + item.Detail + "）"
	}
	fmt.Fprintln(out, line)
}

// paramDefaults 取定义声明的参数默认值。
func paramDefaults(def factorAPIDef) map[string]float64 {
	out := make(map[string]float64, len(def.Params))
	for _, param := range def.Params {
		out[param.Name] = param.DefaultValue
	}
	return out
}

// completeParams 把配置里给出的参数用定义的默认值补全成完整参数。
func completeParams(defaults, given map[string]float64) map[string]float64 {
	out := make(map[string]float64, len(defaults))
	for name, value := range defaults {
		out[name] = value
	}
	for name, value := range given {
		out[name] = value
	}
	return out
}

func sameParamValues(left, right map[string]float64) bool {
	if len(left) != len(right) {
		return false
	}
	for name, value := range left {
		if other, ok := right[name]; !ok || other != value {
			return false
		}
	}
	return true
}

func factorAPINotFound(response factorAPIResponse) bool {
	return response.RetInfo != nil && (response.RetInfo.Code == 9 || response.RetInfo.Code == 5 ||
		(response.RetInfo.Code == 4 && strings.Contains(strings.ToLower(response.RetInfo.Msg), "not found")))
}

func (r *remoteSetupFactor) Close() error {
	if r == nil || r.gateway == nil {
		return nil
	}
	return r.gateway.Close()
}
