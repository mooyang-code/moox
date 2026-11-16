package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	adminclient "github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/topology"
	"github.com/mooyang-code/moox/packages/factorname"
	"github.com/mooyang-code/moox/packages/storagepolicy"
)

// defaultFactorSeed 是仓库里默认因子任务的种子文件，setup start 按空间导入一次。
const defaultFactorSeed = "config/setup/factor-tasks.yaml"

// factorSeed 是因子任务文件（yaml）。默认任务在 config/setup/factor-tasks.yaml，自己的任务写成同样格式的文件，
// 用 factor task apply --file 导入。键名与原来 moox.toml 的 [factors] 三段一致；source_dir 相对这个文件所在的目录。
type factorSeed struct {
	SourceDir   string                 `yaml:"source_dir"`
	Tasks       []factorSeedTask       `yaml:"tasks"`
	Definitions []factorSeedDefinition `yaml:"definitions"`
}

// factorSeedTask 是一个因子任务：id 是任务 ID（ftask_ 加后缀），name 是展示名（直接用作结果数据集的名称），
// 源数据集 + 频率决定它读哪份数据；成员内嵌在任务里。
type factorSeedTask struct {
	ID              string             `yaml:"id"`
	Name            string             `yaml:"name"`
	SpaceID         string             `yaml:"space_id"`
	SourceDatasetID string             `yaml:"source_dataset_id"`
	Freq            string             `yaml:"freq"`
	Members         []factorSeedMember `yaml:"members"`
}

// factorSeedMember 把一个定义加入任务。params 是固定成员的固定参数（没给的取定义的默认值）；var 为真是可变成员，
// 参数由关联的策略实例逐期提交，params 是它的默认参数。status 缺省为 enabled。
type factorSeedMember struct {
	Factor string             `yaml:"factor"`
	Params map[string]float64 `yaml:"params"`
	Var    bool               `yaml:"var"`
	Status string             `yaml:"status"`
}

// factorSeedDefinition 引用因子库文件（source_dir 下的 YAML）里的一个定义。
type factorSeedDefinition struct {
	FactorID string `yaml:"factor_id"`
	File     string `yaml:"file"`
}

// setupFactorTask 是要导入的一个因子任务及其成员。
type setupFactorTask struct {
	TaskID          string
	Name            string
	SpaceID         string
	SourceDatasetID string
	Freq            string
	Members         []setupFactorMember
}

// setupFactorDefinition 是从因子库文件读出的全局定义：不带任务和状态，那些属于成员。
type setupFactorDefinition struct {
	FactorID             string
	Name                 string
	Spec                 string
	AllowPartialUniverse bool
	// CategoryID 是因子分类（空为未分类）；CategoryName 非空时线上没有这个分类就建立它。
	CategoryID   string
	CategoryName string
}

// setupFactorMember 是任务里的一个成员。Kind 是 fixed 或 var；成员键由服务端推导。
type setupFactorMember struct {
	FactorID string
	Kind     string
	Params   map[string]float64
	Status   string
}

// setupFactorPlan 是一次导入的内容：先分类与定义，再任务，最后任务里的成员。
type setupFactorPlan struct {
	Tasks       []setupFactorTask
	Definitions []setupFactorDefinition
	// Warnings 是不阻止导入但用户应该知道的配置关联问题。
	Warnings []string
}

func (p setupFactorPlan) memberCount() int {
	count := 0
	for _, task := range p.Tasks {
		count += len(task.Members)
	}
	return count
}

// libraryEntry 是 YAML 因子库文件里的一项。
type libraryEntry struct {
	FactorID             string `yaml:"factor_id"`
	Name                 string `yaml:"name"`
	CategoryID           string `yaml:"category_id"`
	CategoryName         string `yaml:"category_name"`
	Spec                 string `yaml:"spec"`
	AllowPartialUniverse bool   `yaml:"allow_partial_universe"`
}

// decodeYAMLStrict 严格解码恰好一个 YAML 文档：未知字段、多文档都报错。
func decodeYAMLStrict(raw []byte, out any, name string) error {
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(out); err != nil {
		return fmt.Errorf("parse %s: %w", name, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%s must contain exactly one YAML document", name)
	}
	return nil
}

// readFactorLibrary 严格解析一个因子库文件：缺少 factor_id/name/spec、重复 factor_id 都报错。
func readFactorLibrary(path string) (map[string]libraryEntry, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entries []libraryEntry
	if err := decodeYAMLStrict(raw, &entries, "factor library "+path); err != nil {
		return nil, err
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

// loadFactorPlan 读因子任务文件，按 source_dir 读因子库，并核对回看根数与保留期（setup_retention.go）。
// spaceID 不为空时只保留这个空间的任务；定义是全局的，全部带上。
func loadFactorPlan(path string, policy storagepolicy.Policy, spaceID string) (setupFactorPlan, error) {
	var plan setupFactorPlan
	raw, err := os.ReadFile(path)
	if err != nil {
		return plan, fmt.Errorf("读不到因子任务文件 %s：%w", path, err)
	}
	var seed factorSeed
	if err := decodeYAMLStrict(raw, &seed, path); err != nil {
		return plan, err
	}
	if err := seed.validate(); err != nil {
		return plan, fmt.Errorf("%s：%w", path, err)
	}
	root := seed.SourceDir
	if !filepath.IsAbs(root) {
		root = filepath.Join(filepath.Dir(path), root)
	}
	resolvedRoot, err := filepath.Abs(root)
	if err == nil {
		resolvedRoot, err = filepath.EvalSymlinks(resolvedRoot)
	}
	if err != nil {
		return plan, fmt.Errorf("因子库目录 %s 不存在：%w", seed.SourceDir, err)
	}
	libraries := map[string]map[string]libraryEntry{}
	for _, item := range seed.Definitions {
		resolvedPath, err := filepath.Abs(filepath.Join(resolvedRoot, filepath.FromSlash(item.File)))
		if err == nil {
			resolvedPath, err = filepath.EvalSymlinks(resolvedPath)
		}
		if err != nil || !pathWithin(resolvedRoot, resolvedPath) {
			return plan, fmt.Errorf("factor %q file must stay under source_dir", item.FactorID)
		}
		library, loaded := libraries[resolvedPath]
		if !loaded {
			if library, err = readFactorLibrary(resolvedPath); err != nil {
				return plan, fmt.Errorf("read factor %q library: %w", item.FactorID, err)
			}
			libraries[resolvedPath] = library
		}
		entry, ok := library[item.FactorID]
		if !ok {
			return plan, fmt.Errorf("factor library %s has no factor %q", item.File, item.FactorID)
		}
		plan.Definitions = append(plan.Definitions, setupFactorDefinition{
			FactorID: item.FactorID, Name: entry.Name, Spec: entry.Spec, AllowPartialUniverse: entry.AllowPartialUniverse,
			CategoryID: entry.CategoryID, CategoryName: entry.CategoryName,
		})
	}
	sort.Slice(plan.Definitions, func(i, j int) bool { return plan.Definitions[i].FactorID < plan.Definitions[j].FactorID })
	for _, task := range seed.Tasks {
		if spaceID != "" && task.SpaceID != spaceID {
			continue
		}
		planned := setupFactorTask{TaskID: task.ID, Name: task.Name, SpaceID: task.SpaceID, SourceDatasetID: task.SourceDatasetID, Freq: task.Freq}
		for _, member := range task.Members {
			kind := "fixed"
			if member.Var {
				kind = "var"
			}
			planned.Members = append(planned.Members, setupFactorMember{FactorID: member.Factor, Kind: kind, Params: member.Params, Status: member.Status})
		}
		plan.Tasks = append(plan.Tasks, planned)
	}
	sort.Slice(plan.Tasks, func(i, j int) bool { return plan.Tasks[i].TaskID < plan.Tasks[j].TaskID })
	// 保留期必须大于因子的最大回看根数：两处配置是关联的，导入前核对。
	errs, warnings := factorRetentionFindings(policy, plan)
	if len(errs) > 0 {
		return plan, fmt.Errorf("config_invalid: %s", joinFindings(errs))
	}
	plan.Warnings = warnings
	return plan, nil
}

// validate 规整并检查因子任务文件：定义与任务不能重复，成员引用的定义要声明，参数有限，状态合法。
func (s *factorSeed) validate() error {
	s.SourceDir = strings.TrimSpace(s.SourceDir)
	if s.SourceDir == "" {
		return errors.New("source_dir 不能为空（因子库目录，相对这个文件所在的目录）")
	}
	if len(s.Definitions) == 0 || len(s.Tasks) == 0 {
		return errors.New("要同时写 tasks 与 definitions")
	}
	seen := make(map[string]struct{}, len(s.Definitions))
	for index := range s.Definitions {
		item := &s.Definitions[index]
		path := fmt.Sprintf("definitions[%d]", index)
		item.FactorID = strings.TrimSpace(item.FactorID)
		item.File = filepath.ToSlash(filepath.Clean(strings.TrimSpace(item.File)))
		if err := factorname.ValidateFactorID(item.FactorID); err != nil {
			return fmt.Errorf("%s.factor_id is invalid: %w", path, err)
		}
		if item.File == "" || item.File == "." || filepath.IsAbs(item.File) || item.File == ".." || strings.HasPrefix(item.File, "../") {
			return fmt.Errorf("%s.file must stay under source_dir", path)
		}
		if strings.ToLower(filepath.Ext(item.File)) != ".yaml" {
			return fmt.Errorf("%s.file must be a YAML factor library file (*.yaml)", path)
		}
		if _, ok := seen[item.FactorID]; ok {
			return fmt.Errorf("factor definition %q is duplicated", item.FactorID)
		}
		seen[item.FactorID] = struct{}{}
	}
	taskIDs := make(map[string]struct{}, len(s.Tasks))
	taskNames := make(map[string]struct{}, len(s.Tasks))
	for index := range s.Tasks {
		task := &s.Tasks[index]
		path := fmt.Sprintf("tasks[%d]", index)
		task.ID, task.Name = strings.TrimSpace(task.ID), strings.TrimSpace(task.Name)
		task.SpaceID, task.SourceDatasetID, task.Freq = strings.TrimSpace(task.SpaceID), strings.TrimSpace(task.SourceDatasetID), strings.TrimSpace(task.Freq)
		if task.ID == "" || task.Name == "" || task.SpaceID == "" || task.SourceDatasetID == "" || task.Freq == "" {
			return fmt.Errorf("%s requires id, name, space_id, source_dataset_id and freq", path)
		}
		if _, err := factorname.ParseTaskID(task.ID); err != nil {
			return fmt.Errorf("%s.id is invalid: %v", path, err)
		}
		if err := validateFactorTaskName(task.Name); err != nil {
			return fmt.Errorf("%s.name is invalid: %v", path, err)
		}
		if _, ok := taskIDs[task.ID]; ok {
			return fmt.Errorf("factor task id %q is duplicated", task.ID)
		}
		taskIDs[task.ID] = struct{}{}
		// 任务名直接用作结果数据集名称，空间内唯一。
		nameKey := task.SpaceID + "\x00" + task.Name
		if _, ok := taskNames[nameKey]; ok {
			return fmt.Errorf("factor task name %q is duplicated in space %q", task.Name, task.SpaceID)
		}
		taskNames[nameKey] = struct{}{}
		if err := validateFactorSeedMembers(path, task.Members, seen); err != nil {
			return err
		}
	}
	return nil
}

// validateFactorSeedMembers 检查任务内嵌的成员：同一定义的可变成员最多一个，固定成员的参数完全相同算重复。
// 写法不同但推导出同一个成员键的情况由服务端在加成员时报告。
func validateFactorSeedMembers(path string, members []factorSeedMember, definitions map[string]struct{}) error {
	keys := make(map[string]struct{}, len(members))
	for index := range members {
		member := &members[index]
		memberPath := fmt.Sprintf("%s.members[%d]", path, index)
		member.Factor, member.Status = strings.TrimSpace(member.Factor), strings.TrimSpace(member.Status)
		if member.Factor == "" {
			return fmt.Errorf("%s requires factor", memberPath)
		}
		if _, ok := definitions[member.Factor]; !ok {
			return fmt.Errorf("%s references unknown factor %q; declare it in definitions", memberPath, member.Factor)
		}
		for name, value := range member.Params {
			if strings.TrimSpace(name) == "" || math.IsNaN(value) || math.IsInf(value, 0) {
				return fmt.Errorf("%s.params has an empty name or a non-finite value", memberPath)
			}
		}
		kind := "fixed"
		if member.Var {
			kind = "var"
		}
		key := member.Factor + "\x00" + kind
		if !member.Var {
			key += "\x00" + canonicalParams(member.Params)
		}
		if _, ok := keys[key]; ok {
			return fmt.Errorf("%s repeats factor %q (%s) with the same params", memberPath, member.Factor, kind)
		}
		keys[key] = struct{}{}
		if member.Status == "" {
			member.Status = "enabled"
		}
		if member.Status != "enabled" && member.Status != "disabled" {
			return fmt.Errorf("%s.status must be enabled or disabled", memberPath)
		}
	}
	return nil
}

// validateFactorTaskName 与 FactorMgr 的任务名规则一致：1～10 个字、含汉字、首尾无空白、无控制字符。
func validateFactorTaskName(name string) error {
	if name == "" || name != strings.TrimSpace(name) || utf8.RuneCountInString(name) > 10 {
		return errors.New("must be 1-10 characters without surrounding whitespace")
	}
	hasHan := false
	for _, r := range name {
		if unicode.IsControl(r) {
			return errors.New("must not contain control characters")
		}
		if unicode.Is(unicode.Han, r) {
			hasHan = true
		}
	}
	if !hasHan {
		return errors.New("must contain Chinese characters")
	}
	return nil
}

// canonicalParams 把固定参数写成键有序的文本，用来判断两个成员的参数是否相同。
func canonicalParams(params map[string]float64) string {
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		b.WriteString(name + "=" + strconv.FormatFloat(params[name], 'f', -1, 64) + ";")
	}
	return b.String()
}

func pathWithin(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

func newFactorTaskApplyCommand() *cobra.Command {
	var file, manifest string
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "按因子任务文件新建因子定义、任务与成员（只建不改；线上已有且内容不同时逐项列出差异，一项都不写）",
		Long: `读因子任务文件（yaml，格式同仓库的 config/setup/factor-tasks.yaml），先只读核对线上已有的分类、定义、任务和成员：
- 线上没有的新建；已有且内容相同的算已存在；
- 已有但内容不同（定义的名称、表达式或分类，任务的空间、源数据集、频率或名称，成员的状态或可变成员的默认参数）时，逐项列出差异，一项都不写，退出码非 0；
- 线上任务里有、文件里没有的成员只报告，不移除。
导入前按 moox.toml 的 [storage_retention] 核对：保留期必须装得下因子的最大回看根数。逐项结果打到标准错误，摘要（JSON）打到标准输出。`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cmd.SilenceUsage = true
			cfg, err := topology.Load(manifest)
			if err != nil {
				return err
			}
			plan, err := loadFactorPlan(file, cfg.StoragePolicy(), "")
			if err != nil {
				return err
			}
			reportFactorWarnings(plan.Warnings)
			gateway, err := openCommandGateway(cmd.Context(), manifest, cfg)
			if err != nil {
				return err
			}
			defer gateway.Close()
			applier := &remoteSetupFactor{client: &adminclient.Client{Gateway: gateway}, out: cmd.ErrOrStderr()}
			summary, applyErr := applier.Apply(cmd.Context(), plan)
			summary.Warnings = plan.Warnings
			status := "applied"
			if applyErr != nil {
				status = "failed"
				if summary.Conflicts > 0 {
					status = "conflict"
				}
			}
			if err := writeSetupJSON(cmd, map[string]any{"status": status, "factors": summary}); err != nil {
				return err
			}
			return applyErr
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "因子任务文件（yaml），格式同 config/setup/factor-tasks.yaml")
	cmd.Flags().StringVar(&manifest, "config", defaultSetupFile, "部署配置文件 moox.toml：解析操作员 SSH 网关、核对保留期")
	_ = cmd.MarkFlagRequired("file")
	return cmd
}

// 逐项结果。
const (
	itemExists     = "exists"      // 已存在且一致
	itemCreated    = "created"     // 新建
	itemConflict   = "conflict"    // 与线上的不一致
	itemOrphan     = "orphan"      // 线上任务里有、文件里没有的成员：只报告，不移除
	itemNotWritten = "not_written" // 线上没有，但因为有冲突整份没有写入
)

// setupFactorItem 是一项的处理结果：分类、定义、任务或成员。成员项带成员键与全部列名。
type setupFactorItem struct {
	Kind      string   `json:"kind"`
	ID        string   `json:"id"`
	Result    string   `json:"result"`
	Detail    string   `json:"detail,omitempty"`
	MemberKey string   `json:"member_key,omitempty"`
	Columns   []string `json:"columns,omitempty"`
}

type setupFactorSummary struct {
	Definitions        int `json:"definitions"`
	Tasks              int `json:"tasks"`
	Members            int `json:"members"`
	DefinitionsCreated int `json:"definitions_created"`
	DefinitionsExist   int `json:"definitions_existing"`
	TasksCreated       int `json:"tasks_created"`
	TasksExist         int `json:"tasks_existing"`
	MembersCreated     int `json:"members_created"`
	MembersExist       int `json:"members_existing"`
	Conflicts          int `json:"conflicts"`
	// Items 逐项列出已存在、新建、冲突，以及线上多出来的成员。
	Items []setupFactorItem `json:"items,omitempty"`
	// Warnings 是配置关联的提醒，不阻止导入。
	Warnings []string `json:"warnings,omitempty"`
}

type setupInitFactor interface {
	Apply(context.Context, setupFactorPlan) (setupFactorSummary, error)
	Close() error
}

type remoteSetupFactor struct {
	gateway commandGateway
	client  factorJSONClient
	// out 接收逐项结果；为空时写 stderr。
	out io.Writer
}

type factorJSONClient interface {
	CallGatewayJSON(context.Context, string, string, any, any) error
}

func defaultOpenSetupFactor(ctx context.Context, cfg *topology.Config) (setupInitFactor, error) {
	gateway, err := openCommandGateway(ctx, "", cfg)
	if err != nil {
		return nil, err
	}
	return &remoteSetupFactor{gateway: gateway, client: &adminclient.Client{Gateway: gateway}}, nil
}

type factorAPIRetInfo struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// factorAPIDef 对应 FactorDef 的 proto JSON：定义不带任务和状态。
type factorAPIDef struct {
	FactorID             string `json:"factor_id"`
	Name                 string `json:"name"`
	CategoryID           string `json:"category_id"`
	AllowPartialUniverse bool   `json:"allow_partial_universe"`
	DefinitionHash       string `json:"definition_hash"`
	// Params 是定义声明的可调参数；默认值用来补全成员的参数，判断文件里的成员是否已经存在。
	Params []factorAPIParam `json:"params"`
}

// factorAPIParam 对应 FactorParamDecl 的 proto JSON（只取 CLI 用到的字段）。
type factorAPIParam struct {
	Name         string  `json:"name"`
	DefaultValue float64 `json:"default_value"`
}

// factorAPIMember 对应 FactorMember 的 proto JSON。
type factorAPIMember struct {
	TaskID        string             `json:"task_id"`
	FactorID      string             `json:"factor_id"`
	MemberKey     string             `json:"member_key"`
	Kind          string             `json:"kind"`
	Status        string             `json:"status"`
	Params        map[string]float64 `json:"params"`
	OutputColumns []string           `json:"output_columns"`
}

// factorAPICategory 对应 FactorCategory 的 proto JSON。
type factorAPICategory struct {
	CategoryID string `json:"category_id"`
	Name       string `json:"name"`
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
	Members    []factorAPIMember   `json:"members"`
	Member     factorAPIMember     `json:"member"`
	Categories []factorAPICategory `json:"categories"`
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
		return errors.New("FactorMgr requires the operator SSH gateway")
	}
	if err := r.client.CallGatewayJSON(ctx, factorMgr, method, body, response); err != nil {
		return err
	}
	return response.err(method)
}

// factorWrites 是只读核对后要写的内容，按写入顺序：分类、定义、任务（新建或接着建）、成员。
type factorWrites struct {
	categories  []setupFactorDefinition
	definitions []setupFactorDefinition
	tasks       []factorTaskWrite
}

type factorTaskWrite struct {
	task setupFactorTask
	// create 为真时建任务；resume 表示是接着完成上次被中断的创建，不算新建。members 是要加的成员及其逐项结果的序号。
	create, resume bool
	members        []factorMemberWrite
}

type factorMemberWrite struct {
	member setupFactorMember
	item   int
}

// Apply 只建不改：先只读核对线上已有的内容，算出要新建的项；有任何一项与线上不同就逐项列出、一个写调用都不发，
// 返回错误。没有冲突时按分类、定义、任务、成员的顺序新建缺的项。
func (r *remoteSetupFactor) Apply(ctx context.Context, plan setupFactorPlan) (setupFactorSummary, error) {
	summary := setupFactorSummary{Definitions: len(plan.Definitions), Tasks: len(plan.Tasks), Members: plan.memberCount()}
	writes, err := r.review(ctx, plan, &summary)
	if err != nil {
		return summary, err
	}
	if summary.Conflicts > 0 {
		for index := range summary.Items {
			if summary.Items[index].Result == itemCreated {
				summary.Items[index].Result = itemNotWritten
			}
		}
		r.reportAll(summary.Items)
		return summary, fmt.Errorf("因子任务文件有 %d 项与线上已有的内容不同，一项都没有写入；见摘要 items 里 result=conflict 的条目", summary.Conflicts)
	}
	err = r.write(ctx, writes, &summary)
	r.reportAll(summary.Items)
	return summary, err
}

// review 只发读调用（List/Get/Validate），逐项记录结果，返回要写的内容。
func (r *remoteSetupFactor) review(ctx context.Context, plan setupFactorPlan, summary *setupFactorSummary) (factorWrites, error) {
	var writes factorWrites
	record := func(item setupFactorItem) int {
		summary.Items = append(summary.Items, item)
		if item.Result == itemConflict {
			summary.Conflicts++
		}
		return len(summary.Items) - 1
	}
	// 分类先于定义：定义引用的分类必须存在。同一分类在多个定义上出现时名称以第一次为准。
	wanted := map[string]bool{}
	var categories []setupFactorDefinition
	for _, def := range plan.Definitions {
		if def.CategoryName != "" && !wanted[def.CategoryID] {
			wanted[def.CategoryID] = true
			categories = append(categories, def)
		}
	}
	if len(categories) > 0 {
		listed := factorAPIResponse{}
		if err := r.call(ctx, "ListFactorCategories", map[string]any{}, &listed); err != nil {
			return writes, err
		}
		existing := map[string]string{}
		for _, category := range listed.Categories {
			existing[category.CategoryID] = category.Name
		}
		for _, def := range categories {
			name, ok := existing[def.CategoryID]
			switch {
			case !ok:
				writes.categories = append(writes.categories, def)
				record(setupFactorItem{Kind: "category", ID: def.CategoryID, Result: itemCreated, Detail: def.CategoryName})
			case name != def.CategoryName:
				record(setupFactorItem{Kind: "category", ID: def.CategoryID, Result: itemConflict, Detail: fmt.Sprintf("名称：线上 %q，文件 %q", name, def.CategoryName)})
			default:
				record(setupFactorItem{Kind: "category", ID: def.CategoryID, Result: itemExists})
			}
		}
	}
	// defaults 是线上已有定义声明的参数默认值，用来把文件里的参数补全，再与现有成员比较；changed 是与线上不同的定义。
	defaults := make(map[string]map[string]float64, len(plan.Definitions))
	changed := map[string]bool{}
	for _, def := range plan.Definitions {
		// 新旧定义都先校验：表达式不对时写入之前就停下，不会只写一半。
		validated := factorAPIResponse{}
		if err := r.call(ctx, "ValidateFactorSpec", map[string]any{"spec": def.Spec, "allow_partial_universe": def.AllowPartialUniverse}, &validated); err != nil {
			return writes, err
		}
		if !validated.Valid {
			return writes, fmt.Errorf("factor %q spec is invalid: %s", def.FactorID, validated.Diagnostic.Message)
		}
		get := factorAPIResponse{}
		err := r.call(ctx, "GetFactor", map[string]any{"factor_id": def.FactorID}, &get)
		if err != nil && !factorAPINotFound(get) {
			return writes, err
		}
		if err != nil {
			writes.definitions = append(writes.definitions, def)
			record(setupFactorItem{Kind: "definition", ID: def.FactorID, Result: itemCreated})
			continue
		}
		// 比较用 definition_hash：由 FactorMgr 编译得到，只改注释、格式不算不同。
		var diffs []string
		if get.Factor.Name != def.Name {
			diffs = append(diffs, fmt.Sprintf("名称：线上 %q，文件 %q", get.Factor.Name, def.Name))
		}
		if get.Factor.DefinitionHash != validated.DefinitionHash {
			diffs = append(diffs, "表达式或参数不同（definition_hash 线上 "+get.Factor.DefinitionHash+"，文件 "+validated.DefinitionHash+"）")
		}
		if def.CategoryID != "" && get.Factor.CategoryID != def.CategoryID {
			diffs = append(diffs, fmt.Sprintf("分类：线上 %q，文件 %q", get.Factor.CategoryID, def.CategoryID))
		}
		if len(diffs) > 0 {
			changed[def.FactorID] = true
			record(setupFactorItem{Kind: "definition", ID: def.FactorID, Result: itemConflict, Detail: strings.Join(diffs, "；")})
			continue
		}
		defaults[def.FactorID] = paramDefaults(get.Factor)
		record(setupFactorItem{Kind: "definition", ID: def.FactorID, Result: itemExists})
	}
	for _, task := range plan.Tasks {
		write, err := r.reviewTask(ctx, task, defaults, changed, record)
		if err != nil {
			return writes, err
		}
		if write.create || len(write.members) > 0 {
			writes.tasks = append(writes.tasks, write)
		}
	}
	for _, item := range summary.Items {
		if item.Result != itemExists {
			continue
		}
		switch item.Kind {
		case "definition":
			summary.DefinitionsExist++
		case "task":
			summary.TasksExist++
		case "member":
			summary.MembersExist++
		}
	}
	return writes, nil
}

// reviewTask 核对一个任务：线上没有就整个新建；身份不同记冲突；已有时逐个核对成员。
func (r *remoteSetupFactor) reviewTask(ctx context.Context, task setupFactorTask, defaults map[string]map[string]float64, changed map[string]bool, record func(setupFactorItem) int) (factorTaskWrite, error) {
	write := factorTaskWrite{task: task}
	get := factorAPIResponse{}
	err := r.call(ctx, "GetFactorTask", map[string]any{"task_id": task.TaskID}, &get)
	if err != nil && !factorAPINotFound(get) {
		return write, err
	}
	addMember := func(member setupFactorMember) {
		index := record(setupFactorItem{Kind: "member", ID: task.TaskID + "/" + member.FactorID, Result: itemCreated})
		write.members = append(write.members, factorMemberWrite{member: member, item: index})
	}
	if err != nil {
		write.create = true
		record(setupFactorItem{Kind: "task", ID: task.TaskID, Result: itemCreated, Detail: task.Name})
		for _, member := range task.Members {
			addMember(member)
		}
		return write, nil
	}
	got := get.FactorTask
	var diffs []string
	for _, field := range []struct{ name, online, file string }{
		{"空间", got.SpaceID, task.SpaceID}, {"源数据集", got.SourceDatasetID, task.SourceDatasetID}, {"频率", got.Freq, task.Freq}, {"名称", got.Name, task.Name},
	} {
		if field.online != field.file {
			diffs = append(diffs, fmt.Sprintf("%s：线上 %q，文件 %q", field.name, field.online, field.file))
		}
	}
	if len(diffs) > 0 {
		record(setupFactorItem{Kind: "task", ID: task.TaskID, Result: itemConflict, Detail: strings.Join(diffs, "；") + "（任务身份创建后不能改）"})
		return write, nil
	}
	detail := ""
	if got.Status == "pending" {
		// 上次创建被中断，任务停在 pending；CreateFactorTask 会接着建结果数据集。
		write.create, write.resume = true, true
		detail = "上次创建被中断，会接着完成"
	}
	record(setupFactorItem{Kind: "task", ID: task.TaskID, Result: itemExists, Detail: detail})
	existing := get.Members
	matched := map[string]bool{}
	for _, planned := range task.Members {
		label := task.TaskID + "/" + planned.FactorID
		if changed[planned.FactorID] {
			// 定义已经记了冲突，整份不会写入；这个成员没法按线上的参数默认值核对。
			continue
		}
		decls, known := defaults[planned.FactorID]
		if !known {
			// 定义是这次新建的，任务里不可能已有它的成员。
			addMember(planned)
			continue
		}
		want := completeParams(decls, planned.Params)
		var found, sameFactorVar *factorAPIMember
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
		switch {
		case found == nil && sameFactorVar != nil:
			matched[sameFactorVar.MemberKey] = true
			record(setupFactorItem{Kind: "member", ID: label, Result: itemConflict, MemberKey: sameFactorVar.MemberKey,
				Detail: "可变成员已存在但默认参数不同；参数加成员之后不能改，需要的话先移除成员再重新加入"})
		case found == nil:
			addMember(planned)
		case found.Status != planned.Status:
			matched[found.MemberKey] = true
			record(setupFactorItem{Kind: "member", ID: label, Result: itemConflict, MemberKey: found.MemberKey,
				Detail: fmt.Sprintf("状态：线上 %s，文件 %s", found.Status, planned.Status)})
		default:
			matched[found.MemberKey] = true
			record(setupFactorItem{Kind: "member", ID: label, Result: itemExists, MemberKey: found.MemberKey, Columns: found.OutputColumns})
		}
	}
	for _, member := range existing {
		if !matched[member.MemberKey] {
			record(setupFactorItem{Kind: "member", ID: task.TaskID + "/" + member.FactorID, Result: itemOrphan, MemberKey: member.MemberKey,
				Detail: "线上任务里有、文件里没有；只报告，不移除"})
		}
	}
	return write, nil
}

// write 按顺序新建核对出来的缺项；成员加好后按文件里的状态启用或停用，并把成员键与列名写回逐项结果。
func (r *remoteSetupFactor) write(ctx context.Context, writes factorWrites, summary *setupFactorSummary) error {
	for _, def := range writes.categories {
		upserted := factorAPIResponse{}
		if err := r.call(ctx, "UpsertFactorCategory", map[string]any{"category": map[string]any{"category_id": def.CategoryID, "name": def.CategoryName}}, &upserted); err != nil {
			return err
		}
	}
	for _, def := range writes.definitions {
		created := factorAPIResponse{}
		if err := r.call(ctx, "CreateFactor", map[string]any{"factor": map[string]any{
			"factor_id": def.FactorID, "name": def.Name, "spec": def.Spec,
			"allow_partial_universe": def.AllowPartialUniverse, "category_id": def.CategoryID,
		}}, &created); err != nil {
			return err
		}
		summary.DefinitionsCreated++
	}
	for _, write := range writes.tasks {
		task := write.task
		if write.create {
			created := factorAPIResponse{}
			if err := r.call(ctx, "CreateFactorTask", map[string]any{"factor_task": map[string]any{
				"task_id": task.TaskID, "name": task.Name, "space_id": task.SpaceID, "source_dataset_id": task.SourceDatasetID,
				"freq": task.Freq, "status": "pending",
			}}, &created); err != nil {
				return err
			}
			if !write.resume {
				summary.TasksCreated++
			}
		}
		for _, planned := range write.members {
			body := map[string]any{"task_id": task.TaskID, "factor_id": planned.member.FactorID, "kind": planned.member.Kind}
			if len(planned.member.Params) > 0 {
				body["params"] = planned.member.Params
			}
			added := factorAPIResponse{}
			if err := r.call(ctx, "AddTaskMember", body, &added); err != nil {
				return err
			}
			member := added.Member
			if member.MemberKey == "" {
				return fmt.Errorf("AddTaskMember returned no member key for factor %q in task %q", planned.member.FactorID, task.TaskID)
			}
			if member.Status == "" {
				member.Status = "disabled"
			}
			if member.Status != planned.member.Status {
				status := factorAPIResponse{}
				if err := r.call(ctx, "SetTaskMemberStatus", map[string]any{"task_id": task.TaskID, "member_key": member.MemberKey, "status": planned.member.Status}, &status); err != nil {
					return err
				}
			}
			summary.MembersCreated++
			summary.Items[planned.item].MemberKey, summary.Items[planned.item].Columns = member.MemberKey, member.OutputColumns
		}
	}
	return nil
}

var factorItemLabels = map[string]string{itemExists: "已存在", itemCreated: "新建", itemConflict: "与线上不同", itemOrphan: "文件里没有", itemNotWritten: "线上没有（因有冲突未写入）"}

var factorKindLabels = map[string]string{"category": "分类", "task": "任务", "definition": "定义", "member": "成员"}

// reportAll 把逐项结果逐行打到 out（默认 stderr），成员项带成员键与全部列名。
func (r *remoteSetupFactor) reportAll(items []setupFactorItem) {
	out := r.out
	if out == nil {
		out = os.Stderr
	}
	for _, item := range items {
		line := fmt.Sprintf("因子%s %s：%s", factorKindLabels[item.Kind], item.ID, factorItemLabels[item.Result])
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
}

// paramDefaults 取定义声明的参数默认值。
func paramDefaults(def factorAPIDef) map[string]float64 {
	out := make(map[string]float64, len(def.Params))
	for _, param := range def.Params {
		out[param.Name] = param.DefaultValue
	}
	return out
}

// completeParams 把文件里给出的参数用定义的默认值补全成完整参数。
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
