package configinit

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	setupconfig "github.com/mooyang-code/moox/modules/cli/internal/setup/config"
	"github.com/mooyang-code/moox/modules/cli/internal/setup/configcrypt"
	"github.com/mooyang-code/moox/packages/servicecatalog"
)

const defaultFactorSourceDir = "modules/factor/factors"

// MigrateOptions 控制 config migrate 的行为。
type MigrateOptions struct {
	// RepoRoot 是解析 factors.source_dir 的仓库根目录。
	RepoRoot string
	// RenameFrom / RenameTo 非空时把主机 ID 改名（--rename-host old=new）。
	RenameFrom, RenameTo string
	// TLSModeHost / TLSMode 非空时设置该主机的控制台证书模式（--set-tls-mode 主机=auto|public|internal）。
	TLSModeHost, TLSMode string
	// FactorsEnabled 非空（"true"/"false"）时设置 [factors] enabled（--set-factors-enabled）。
	FactorsEnabled string
	// ReachabilityHost / Reachability 非空时设置该主机的可达性（--set-reachability 主机=direct|outbound）。
	ReachabilityHost, Reachability string
	// AddDeploymentHost / AddDeploymentComponents 非空时把这些组件加进该主机的 [deployments] 条目（--add-deployment 主机=组件,组件）。
	AddDeploymentHost       string
	AddDeploymentComponents []string
	// SCFImport 非空时用这份 JSON 整体替换 [scf_plan]，并给相关主机补上 region 与 private_address（--import-scf）。
	SCFImport string
	// Write 为 true 时校验通过后写回（备份原文件并重新加密）；否则只预览。
	Write bool
	Keys  configcrypt.KeyStore
	// Now 用于备份文件名，测试可替换。
	Now func() time.Time
}

// FactorMigration 是一条因子定义的改写：只保留 factor_id，file 指向包含它的因子库文件。
type FactorMigration struct {
	FactorID    string   `json:"factor_id"`
	File        string   `json:"file"`
	RemovedKeys []string `json:"removed_keys,omitempty"`
}

// HostLabelMigration 是给主机补上的中文名。
type HostLabelMigration struct {
	Host  string `json:"host"`
	Label string `json:"label"`
}

// FactorEngineMigration 记录旧的 [factor_engine] 被转成了哪台主机的只能出站与外部程序部署。
type FactorEngineMigration struct {
	Host string `json:"host"`
}

// HostRename 记录主机 ID 改名涉及的位置数。
type HostRename struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Changes int    `json:"changes"`
}

// ComponentRename 记录 [deployments] 里一个旧组件身份改成当前名字涉及的位置数。
type ComponentRename struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Changes int    `json:"changes"`
}

// legacyComponents 是已经改名的组件身份：旧名字不再出现在服务目录里，[deployments] 里还写旧名字会让所有命令拒绝配置。
var legacyComponents = map[string]string{"cloudnode": "cloudfunction"}

// SCFImportReport 记录导入的 SCF 计划概要（地域、空间与市场、更新了网络信息的主机），不含任何配置值。
type SCFImportReport struct {
	Regions []string            `json:"regions"`
	Spaces  map[string][]string `json:"spaces"`
	Hosts   []string            `json:"hosts"`
}

// MigrateReport 只包含键名、因子 ID、文件名、主机 ID 与中文名，不含任何配置值。
type MigrateReport struct {
	Path      string               `json:"path"`
	Encrypted bool                 `json:"encrypted"`
	Factors   []FactorMigration    `json:"factors,omitempty"`
	Labels    []HostLabelMigration `json:"labels,omitempty"`
	Rename    *HostRename          `json:"rename,omitempty"`
	// FactorEngine 不为空表示旧的 [factor_engine] 已转成该主机的 reachability = "outbound" 与两个外部程序部署。
	FactorEngine *FactorEngineMigration `json:"factor_engine,omitempty"`
	// Components 记录 [deployments] 里被改成当前名字的旧组件身份（例如 cloudnode → cloudfunction）及位置数。
	Components []ComponentRename `json:"components,omitempty"`
	// Deployments 为 true 表示旧的 [placements] 表被改名为 [deployments]。
	Deployments bool              `json:"deployments,omitempty"`
	SCF         *SCFImportReport  `json:"scf,omitempty"`
	TLSMode     map[string]string `json:"tls_mode,omitempty"`
	// FactorsEnabled 记录 --set-factors-enabled 的结果。
	FactorsEnabled string `json:"factors_enabled,omitempty"`
	// Reachability 记录 --set-reachability 的结果。
	Reachability map[string]string `json:"reachability,omitempty"`
	// AddedDeployments 记录 --add-deployment 新增到各主机的组件。
	AddedDeployments map[string][]string `json:"added_deployments,omitempty"`
	Changed          bool                `json:"changed"`
	Valid            bool                `json:"valid"`
	Written          bool                `json:"written"`
	Backup           string              `json:"backup,omitempty"`
	NextAction       string              `json:"next_action,omitempty"`
}

// MigrateFile 在本机解密 moox.toml，把它改写成当前格式并校验；Write 时备份原文件、重新加密写回。
// 任何错误信息与报告都不包含配置值。
func MigrateFile(path string, opts MigrateOptions) (MigrateReport, error) {
	report := MigrateReport{Path: path}
	if opts.Keys == nil {
		opts.Keys = configcrypt.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	raw, err := readConfigFile(path)
	if err != nil {
		return report, err
	}
	plain := raw
	if configcrypt.IsEncrypted(raw) {
		report.Encrypted = true
		if plain, err = configcrypt.Decrypt(raw, opts.Keys); err != nil {
			return report, fmt.Errorf("解不开 %s：%w", path, err)
		}
	}
	library, err := loadFactorLibrary(filepath.Join(opts.RepoRoot, filepath.FromSlash(factorSourceDir(plain))))
	if err != nil {
		return report, err
	}
	migrated, err := migrateManifestText(plain, library, opts.RenameFrom, opts.RenameTo, &report)
	if err != nil {
		return report, err
	}
	if opts.TLSModeHost != "" {
		if migrated, err = setHostTLSMode(migrated, opts.TLSModeHost, opts.TLSMode); err != nil {
			return report, err
		}
		report.TLSMode = map[string]string{opts.TLSModeHost: opts.TLSMode}
	}
	if opts.FactorsEnabled != "" {
		if migrated, err = setFactorsEnabled(migrated, opts.FactorsEnabled); err != nil {
			return report, err
		}
		report.FactorsEnabled = opts.FactorsEnabled
	}
	if opts.ReachabilityHost != "" {
		if migrated, err = setHostReachability(migrated, opts.ReachabilityHost, opts.Reachability); err != nil {
			return report, err
		}
		report.Reachability = map[string]string{opts.ReachabilityHost: opts.Reachability}
	}
	if opts.AddDeploymentHost != "" {
		var added []string
		if migrated, added, err = addHostDeployments(migrated, opts.AddDeploymentHost, opts.AddDeploymentComponents); err != nil {
			return report, err
		}
		if len(added) > 0 {
			report.AddedDeployments = map[string][]string{opts.AddDeploymentHost: added}
		}
	}
	if opts.SCFImport != "" {
		plan, err := readSCFImport(opts.SCFImport)
		if err != nil {
			return report, err
		}
		if migrated, err = importSCFPlan(migrated, plan, &report); err != nil {
			return report, err
		}
	}
	report.Changed = !bytes.Equal(migrated, plain)
	if _, err := setupconfig.Parse(migrated); err != nil {
		return report, fmt.Errorf("改写后的配置校验没有通过，未写回：%w", err)
	}
	report.Valid = true
	if !opts.Write || !report.Changed {
		if report.Changed {
			report.NextAction = "确认无误后加 --yes 写回"
		}
		return report, nil
	}
	current, err := readConfigFile(path)
	if err != nil {
		return report, err
	}
	if sha256.Sum256(current) != sha256.Sum256(raw) {
		return report, errors.New("moox.toml 在迁移过程中被改动过，未写回；请重新运行")
	}
	backup := fmt.Sprintf("%s.bak-%s", path, opts.Now().Format("20060102150405"))
	if err := writeExclusive(backup, raw); err != nil {
		return report, err
	}
	report.Backup = backup
	sealed, err := configcrypt.Encrypt(migrated, opts.Keys)
	if err != nil {
		return report, fmt.Errorf("加密失败：%w", err)
	}
	if err := replaceFile(path, sealed); err != nil {
		return report, err
	}
	report.Written = true
	report.NextAction = "运行一次 moox-cli config backup 更新管理后台的备份"
	return report, nil
}

// writeExclusive 以 0600 新建文件，已存在时拒绝覆盖。
func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("无法创建备份 %s：%w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("写备份 %s 失败：%w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("写备份 %s 失败：%w", path, err)
	}
	return file.Close()
}

// factorSourceDir 读出 [factors] source_dir，没有时用默认目录。
func factorSourceDir(plain []byte) string {
	section := ""
	for _, line := range strings.Split(string(plain), "\n") {
		if name, _, ok := tableHeader(line); ok {
			section = name
			continue
		}
		if section == "factors" {
			if key, value, ok := keyValue(line); ok && key == "source_dir" {
				if dir := strings.Trim(strings.TrimSpace(value), `"'`); dir != "" {
					return strings.TrimPrefix(filepath.ToSlash(filepath.Clean(dir)), "./")
				}
			}
		}
	}
	return defaultFactorSourceDir
}

// loadFactorLibrary 扫描因子库目录，返回 factor_id → 相对文件路径。同一个因子出现在多个文件里视为歧义。
func loadFactorLibrary(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !(strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")) {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var items []struct {
			FactorID string `yaml:"factor_id"`
		}
		if err := yaml.Unmarshal(raw, &items); err != nil {
			return fmt.Errorf("因子库 %s 不是因子列表：%w", path, err)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, item := range items {
			id := strings.TrimSpace(item.FactorID)
			if id == "" {
				continue
			}
			if previous, exists := out[id]; exists {
				return fmt.Errorf("因子 %s 同时出现在 %s 和 %s", id, previous, filepath.ToSlash(rel))
			}
			out[id] = filepath.ToSlash(rel)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("读取因子库目录失败：%w", err)
	}
	return out, nil
}

var (
	headerPattern = regexp.MustCompile(`^\s*(\[\[?)\s*([^\[\]]+?)\s*(\]\]?)\s*(#.*)?$`)
	keyPattern2   = regexp.MustCompile(`^\s*([A-Za-z0-9_-]+|"[^"]*")\s*=\s*(.*)$`)
)

// tableHeader 识别 [name] 或 [[name]]。
func tableHeader(line string) (name string, array bool, ok bool) {
	m := headerPattern.FindStringSubmatch(line)
	if m == nil || len(m[1]) != len(m[3]) {
		return "", false, false
	}
	return m[2], m[1] == "[[", true
}

func keyValue(line string) (key, value string, ok bool) {
	m := keyPattern2.FindStringSubmatch(line)
	if m == nil {
		return "", "", false
	}
	return strings.Trim(m[1], `"`), m[2], true
}

// valueEnd 返回从第 i 行开始的键值占用到的最后一行（多行数组与多行字符串）。
func valueEnd(lines []string, i int) int {
	_, value, ok := keyValue(lines[i])
	if !ok {
		return i
	}
	value = strings.TrimSpace(value)
	for _, quote := range []string{`"""`, `'''`} {
		if strings.HasPrefix(value, quote) && strings.Count(value, quote) < 2 {
			for j := i + 1; j < len(lines); j++ {
				if strings.Contains(lines[j], quote) {
					return j
				}
			}
			return len(lines) - 1
		}
	}
	if strings.HasPrefix(value, "[") {
		depth := strings.Count(value, "[") - strings.Count(value, "]")
		for j := i + 1; depth > 0 && j < len(lines); j++ {
			depth += strings.Count(lines[j], "[") - strings.Count(lines[j], "]")
			if depth <= 0 {
				return j
			}
		}
	}
	return i
}

func quotedValue(value string) string {
	value = strings.TrimSpace(value)
	if i := strings.Index(value, "#"); i > 0 && !strings.HasPrefix(value, `"`) && !strings.HasPrefix(value, "'") {
		value = strings.TrimSpace(value[:i])
	}
	if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') {
		if end := strings.IndexByte(value[1:], value[0]); end >= 0 {
			return value[1 : end+1]
		}
	}
	return value
}

type block struct {
	header string // 表名；文件开头没有表头的部分为空
	array  bool
	lines  []string
}

func splitBlocks(lines []string) []block {
	blocks := []block{{}}
	for _, line := range lines {
		if name, array, ok := tableHeader(line); ok {
			blocks = append(blocks, block{header: name, array: array})
		}
		blocks[len(blocks)-1].lines = append(blocks[len(blocks)-1].lines, line)
	}
	return blocks
}

// migrateManifestText 是纯文本改写：保留注释与其余内容，只改因子定义、主机名与中文名。
func migrateManifestText(plain []byte, library map[string]string, renameFrom, renameTo string, report *MigrateReport) ([]byte, error) {
	blocks := splitBlocks(strings.Split(string(plain), "\n"))
	for _, b := range blocks {
		if b.header == "factors.items" {
			return nil, errors.New("[[factors.items]] 是更早的格式，需要先手工拆成 [[factors.definitions]] 与 [[factors.members]]")
		}
	}
	if err := renameLegacyPlacements(blocks, report); err != nil {
		return nil, err
	}
	var err error
	if blocks, err = migrateFactorEngine(blocks, report); err != nil {
		return nil, err
	}
	if renameFrom != "" || renameTo != "" {
		if err := renameHost(blocks, renameFrom, renameTo, report); err != nil {
			return nil, err
		}
	}
	renameDeploymentComponents(blocks, report)
	for i := range blocks {
		if blocks[i].header == "factors.definitions" && blocks[i].array {
			change, err := migrateFactorBlock(&blocks[i], library)
			if err != nil {
				return nil, err
			}
			if change != nil {
				report.Factors = append(report.Factors, *change)
			}
		}
	}
	for i := range blocks {
		id, ok := strings.CutPrefix(blocks[i].header, "hosts.")
		if !ok || blocks[i].array || strings.Contains(id, ".") {
			continue
		}
		label := setupconfig.DefaultHostLabel(id)
		if label == "" || blockHasKey(blocks[i], "label") {
			continue
		}
		b := &blocks[i]
		b.lines = append([]string{b.lines[0], fmt.Sprintf("label = %q", label)}, b.lines[1:]...)
		report.Labels = append(report.Labels, HostLabelMigration{Host: id, Label: label})
	}
	var out []string
	for _, b := range blocks {
		out = append(out, b.lines...)
	}
	return []byte(strings.Join(out, "\n")), nil
}

// factorExternalPrograms 是因子机上部署的两个外部程序组件。
var factorExternalPrograms = []string{"factor-engine", "archive-worker"}

// migrateFactorEngine 把旧的 [factor_engine] host = "<主机>" 转成该主机的 reachability = "outbound"
// 和 factor-engine、archive-worker 两个部署，并删除 [factor_engine]；local 已不再支持。重复执行结果不变。
func migrateFactorEngine(blocks []block, report *MigrateReport) ([]block, error) {
	at := slices.IndexFunc(blocks, func(b block) bool { return b.header == "factor_engine" && !b.array })
	if at < 0 {
		return blocks, nil
	}
	host := ""
	for _, line := range blocks[at].lines[1:] {
		if key, value, ok := keyValue(line); ok && key == "host" {
			host = strings.TrimSpace(quotedValue(value))
		}
	}
	switch {
	case host == "":
		return nil, errors.New("[factor_engine] 没有写 host：把因子机登记成 [hosts.factor]，加 reachability = \"outbound\"，并在 [deployments] 里部署 factor-engine 与 archive-worker，然后删除 [factor_engine]")
	case host == "local":
		return nil, errors.New("[factor_engine] host = \"local\" 已不再支持：因子引擎只运行在 Linux 主机上。请把因子机登记成 [hosts.factor]（加 reachability = \"outbound\"），在 [deployments] 里部署 factor-engine 与 archive-worker，然后删除 [factor_engine]")
	}
	hostAt := slices.IndexFunc(blocks, func(b block) bool { return b.header == "hosts."+host && !b.array })
	if hostAt < 0 {
		return nil, fmt.Errorf("[factor_engine] 指向的主机 %s 没有登记在 [hosts.%s]", host, host)
	}
	setBlockKey(&blocks[hostAt], "reachability", `"outbound"`)
	deployAt := slices.IndexFunc(blocks, func(b block) bool { return b.header == "deployments" && !b.array })
	if deployAt < 0 {
		return nil, errors.New("moox.toml 里没有 [deployments]，无法部署因子机上的外部程序")
	}
	if err := ensureDeployments(&blocks[deployAt], host, factorExternalPrograms); err != nil {
		return nil, err
	}
	// [factor_engine] 前面紧挨着的说明注释属于它，一并删除。
	previous := blocks[at-1]
	for len(previous.lines) > 0 && strings.HasPrefix(strings.TrimSpace(previous.lines[len(previous.lines)-1]), "#") {
		previous.lines = previous.lines[:len(previous.lines)-1]
	}
	blocks[at-1] = previous
	blocks = slices.Delete(blocks, at, at+1)
	report.FactorEngine = &FactorEngineMigration{Host: host}
	return blocks, nil
}

// ensureDeployments 保证 [deployments] 里主机 host 的单行数组包含 components；没有这个键时追加一行。
func ensureDeployments(b *block, host string, components []string) error {
	for i := 1; i < len(b.lines); i++ {
		key, value, ok := keyValue(b.lines[i])
		if !ok || key != host {
			continue
		}
		if valueEnd(b.lines, i) != i || !strings.HasSuffix(strings.TrimSpace(value), "]") {
			return fmt.Errorf("[deployments] 里 %s 的组件列表写成了多行，请手工加上 %s 后再运行", host, strings.Join(components, "、"))
		}
		var wanted []string
		for _, match := range componentPattern.FindAllStringSubmatch(value, -1) {
			wanted = append(wanted, match[1])
		}
		for _, component := range components {
			if !slices.Contains(wanted, component) {
				wanted = append(wanted, component)
			}
		}
		quoted := make([]string, len(wanted))
		for j, id := range wanted {
			quoted[j] = fmt.Sprintf("%q", id)
		}
		b.lines[i] = host + " = [" + strings.Join(quoted, ", ") + "]"
		return nil
	}
	quoted := make([]string, len(components))
	for j, id := range components {
		quoted[j] = fmt.Sprintf("%q", id)
	}
	line := host + " = [" + strings.Join(quoted, ", ") + "]"
	end := len(b.lines)
	for end > 1 && strings.TrimSpace(b.lines[end-1]) == "" {
		end--
	}
	b.lines = append(b.lines[:end], append([]string{line}, b.lines[end:]...)...)
	return nil
}

var componentPattern = regexp.MustCompile(`["']([^"']+)["']`)

// renameLegacyPlacements 把旧的 [placements] 表改名为 [deployments]；两张表同时存在时拒绝，避免悄悄丢掉其中一份。
func renameLegacyPlacements(blocks []block, report *MigrateReport) error {
	legacy := -1
	for i, b := range blocks {
		switch b.header {
		case "placements":
			legacy = i
		case "deployments":
			for _, other := range blocks {
				if other.header == "placements" {
					return errors.New("moox.toml 里同时有 [placements] 与 [deployments]，请手工合并后再运行")
				}
			}
		}
	}
	if legacy < 0 {
		return nil
	}
	blocks[legacy].lines[0] = strings.Replace(blocks[legacy].lines[0], "placements", "deployments", 1)
	blocks[legacy].header = "deployments"
	report.Deployments = true
	return nil
}

// renameDeploymentComponents 把 [deployments] 各主机组件列表里的旧组件身份改成当前名字。
func renameDeploymentComponents(blocks []block, report *MigrateReport) {
	counts := map[string]int{}
	for i := range blocks {
		if blocks[i].header != "deployments" {
			continue
		}
		for j := 1; j < len(blocks[i].lines); j++ {
			// 多行数组的续行没有 key = value，所以逐行处理（跳过注释行）；双引号与单引号字符串都改。
			if strings.HasPrefix(strings.TrimSpace(blocks[i].lines[j]), "#") {
				continue
			}
			for from, to := range legacyComponents {
				for _, quote := range []string{`"`, `'`} {
					old := quote + from + quote
					if strings.Contains(blocks[i].lines[j], old) {
						blocks[i].lines[j] = strings.ReplaceAll(blocks[i].lines[j], old, quote+to+quote)
						counts[from]++
					}
				}
			}
		}
	}
	froms := make([]string, 0, len(counts))
	for from := range counts {
		froms = append(froms, from)
	}
	sort.Strings(froms)
	for _, from := range froms {
		report.Components = append(report.Components, ComponentRename{From: from, To: legacyComponents[from], Changes: counts[from]})
	}
}

func blockHasKey(b block, key string) bool {
	for _, line := range b.lines[1:] {
		if k, _, ok := keyValue(line); ok && k == key {
			return true
		}
	}
	return false
}

// migrateFactorBlock 把一个 [[factors.definitions]] 改成只有 factor_id 与 file；已经是新格式时不改。
func migrateFactorBlock(b *block, library map[string]string) (*FactorMigration, error) {
	factorID, currentFile := "", ""
	var removed []string
	for _, line := range b.lines[1:] {
		key, value, ok := keyValue(line)
		if !ok {
			continue
		}
		switch key {
		case "factor_id":
			factorID = strings.TrimSpace(quotedValue(value))
		case "file":
			currentFile = strings.TrimSpace(quotedValue(value))
		default:
			removed = append(removed, key)
		}
	}
	if factorID == "" {
		return nil, errors.New("有一个 [[factors.definitions]] 缺少 factor_id")
	}
	file, ok := library[factorID]
	if !ok {
		return nil, fmt.Errorf("因子库里没有因子 %s：先在因子库（YAML）里写好它，或从 moox.toml 删掉这条定义", factorID)
	}
	if len(removed) == 0 && currentFile == file {
		return nil, nil
	}
	kept := []string{b.lines[0]}
	for i := 1; i < len(b.lines); i++ {
		key, _, ok := keyValue(b.lines[i])
		if !ok {
			kept = append(kept, b.lines[i])
			continue
		}
		end := valueEnd(b.lines, i)
		if key == "factor_id" {
			kept = append(kept, b.lines[i:end+1]...)
			kept = append(kept, fmt.Sprintf("file = %q", file))
		}
		i = end
	}
	b.lines = kept
	sort.Strings(removed)
	return &FactorMigration{FactorID: factorID, File: file, RemovedKeys: removed}, nil
}

// renameHost 改主机表名、[deployments] 键以及 access_host / host 引用。
func renameHost(blocks []block, from, to string, report *MigrateReport) error {
	if !servicecatalog.ValidHostID(from) || !servicecatalog.ValidHostID(to) || from == to {
		return errors.New("--rename-host 需要写成 旧ID=新ID，两者都是合法且不同的主机 ID")
	}
	found := false
	for _, b := range blocks {
		switch b.header {
		case "hosts." + from:
			found = true
		case "hosts." + to:
			return fmt.Errorf("已经有主机 %s，不能把 %s 改成它", to, from)
		}
	}
	if !found {
		return fmt.Errorf("moox.toml 里没有主机 %s", from)
	}
	rename := &HostRename{From: from, To: to}
	for i := range blocks {
		b := &blocks[i]
		if b.header == "hosts."+from || strings.HasPrefix(b.header, "hosts."+from+".") {
			newHeader := "hosts." + to + strings.TrimPrefix(b.header, "hosts."+from)
			b.lines[0] = strings.Replace(b.lines[0], b.header, newHeader, 1)
			b.header = newHeader
			rename.Changes++
		}
		for j := 1; j < len(b.lines); j++ {
			key, value, ok := keyValue(b.lines[j])
			if !ok {
				continue
			}
			switch {
			case b.header == "deployments" && key == from:
				b.lines[j] = strings.Replace(b.lines[j], from, to, 1)
				rename.Changes++
			case (key == "access_host" || key == "host") && quotedValue(value) == from:
				b.lines[j] = strings.Replace(b.lines[j], `"`+from+`"`, `"`+to+`"`, 1)
				rename.Changes++
			}
		}
	}
	report.Rename = rename
	return nil
}

// scfImport 是 --import-scf 的 JSON：与 [scf_plan] 同构，另带各入口主机的地域与私网地址。
type scfImport struct {
	CloudAccount struct {
		AccountID          string `json:"account_id"`
		AccountName        string `json:"account_name"`
		CredentialSecretID string `json:"credential_secret_id"`
		AppID              string `json:"app_id"`
		COSRegion          string `json:"cos_region"`
		COSBucket          string `json:"cos_bucket"`
	} `json:"cloud_account"`
	MemoryMB           int `json:"memory_mb"`
	SymbolsPerFunction int `json:"symbols_per_function"`
	AssumedEgressIPs   int `json:"assumed_egress_ips"`
	Regions            map[string]struct {
		VPCID      string `json:"vpc_id"`
		SubnetID   string `json:"subnet_id"`
		AccessHost string `json:"access_host"`
	} `json:"regions"`
	Hosts map[string]struct {
		Region         string `json:"region"`
		PrivateAddress string `json:"private_address"`
	} `json:"hosts"`
	Spaces []struct {
		SpaceID     string              `json:"space_id"`
		Markets     map[string][]string `json:"markets"`
		PackageName string              `json:"package_name"`
		Namespace   string              `json:"namespace"`
	} `json:"spaces"`
}

func readSCFImport(path string) (scfImport, error) {
	var plan scfImport
	raw, err := os.ReadFile(path)
	if err != nil {
		return plan, fmt.Errorf("读不到 SCF 导入文件：%w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return plan, errors.New("SCF 导入文件不是预期的 JSON 结构")
	}
	if len(plan.Regions) == 0 || len(plan.Spaces) == 0 {
		return plan, errors.New("SCF 导入文件缺少地域或空间")
	}
	return plan, nil
}

// importSCFPlan 删除现有的 [scf_plan] 各表，追加导入的计划，并给入口主机写上 region 与 private_address。
func importSCFPlan(plain []byte, plan scfImport, report *MigrateReport) ([]byte, error) {
	blocks := splitBlocks(strings.Split(strings.TrimRight(string(plain), "\n"), "\n"))
	kept := blocks[:0]
	for _, b := range blocks {
		if b.header == "scf_plan" || strings.HasPrefix(b.header, "scf_plan.") {
			continue
		}
		kept = append(kept, b)
	}
	blocks = kept
	summary := &SCFImportReport{Spaces: map[string][]string{}}
	hosts := make([]string, 0, len(plan.Hosts))
	for id := range plan.Hosts {
		hosts = append(hosts, id)
	}
	sort.Strings(hosts)
	for _, id := range hosts {
		values := plan.Hosts[id]
		i := slices.IndexFunc(blocks, func(b block) bool { return b.header == "hosts."+id && !b.array })
		if i < 0 {
			return nil, fmt.Errorf("moox.toml 里没有主机 %s，无法写入它的 SCF 入口网络", id)
		}
		setBlockKey(&blocks[i], "region", fmt.Sprintf("%q", values.Region))
		setBlockKey(&blocks[i], "private_address", fmt.Sprintf("%q", values.PrivateAddress))
		summary.Hosts = append(summary.Hosts, id)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "\n[scf_plan]\nenabled = true\n")
	for _, kv := range []struct {
		key   string
		value int
	}{{"memory_mb", plan.MemoryMB}, {"symbols_per_function", plan.SymbolsPerFunction}, {"assumed_egress_ips", plan.AssumedEgressIPs}} {
		if kv.value > 0 {
			fmt.Fprintf(&b, "%s = %d\n", kv.key, kv.value)
		}
	}
	a := plan.CloudAccount
	fmt.Fprintf(&b, "\n[scf_plan.cloud_account]\naccount_id = %q\naccount_name = %q\ncredential_secret_id = %q\napp_id = %q\ncos_region = %q\ncos_bucket = %q\n",
		a.AccountID, a.AccountName, a.CredentialSecretID, a.AppID, a.COSRegion, a.COSBucket)
	regions := make([]string, 0, len(plan.Regions))
	for name := range plan.Regions {
		regions = append(regions, name)
	}
	sort.Strings(regions)
	for _, name := range regions {
		r := plan.Regions[name]
		fmt.Fprintf(&b, "\n[scf_plan.regions.%s]\nvpc_id = %q\nsubnet_id = %q\naccess_host = %q\n", name, r.VPCID, r.SubnetID, r.AccessHost)
	}
	summary.Regions = regions
	for _, space := range plan.Spaces {
		fmt.Fprintf(&b, "\n[[scf_plan.spaces]]\nspace_id = %q\n", space.SpaceID)
		if space.PackageName != "" {
			fmt.Fprintf(&b, "package_name = %q\n", space.PackageName)
		}
		if space.Namespace != "" {
			fmt.Fprintf(&b, "namespace = %q\n", space.Namespace)
		}
		markets := make([]string, 0, len(space.Markets))
		for market := range space.Markets {
			markets = append(markets, market)
		}
		sort.Strings(markets)
		fmt.Fprintf(&b, "\n[scf_plan.spaces.markets]\n")
		for _, market := range markets {
			quoted := make([]string, 0, len(space.Markets[market]))
			for _, region := range space.Markets[market] {
				quoted = append(quoted, fmt.Sprintf("%q", region))
			}
			fmt.Fprintf(&b, "%s = [%s]\n", market, strings.Join(quoted, ", "))
		}
		summary.Spaces[space.SpaceID] = markets
	}
	var out []string
	for _, blk := range blocks {
		out = append(out, blk.lines...)
	}
	report.SCF = summary
	return []byte(strings.Join(out, "\n") + "\n" + b.String()), nil
}

// setBlockKey 在表里设置一个键：已有就替换那一行，没有就插到表头（以及 label）之后。
func setBlockKey(b *block, key, value string) {
	line := key + " = " + value
	for i := 1; i < len(b.lines); i++ {
		if k, _, ok := keyValue(b.lines[i]); ok && k == key {
			b.lines[i] = line
			return
		}
	}
	at := 1
	if len(b.lines) > 1 {
		if k, _, ok := keyValue(b.lines[1]); ok && k == "label" {
			at = 2
		}
	}
	b.lines = append(b.lines[:at], append([]string{line}, b.lines[at:]...)...)
}

// setFactorsEnabled 设置 [factors] 的 enabled；没有 [factors] 表时报错，不凭空创建。
func setFactorsEnabled(plain []byte, value string) ([]byte, error) {
	if value != "true" && value != "false" {
		return nil, errors.New("--set-factors-enabled 的取值只能是 true 或 false")
	}
	blocks := splitBlocks(strings.Split(string(plain), "\n"))
	i := slices.IndexFunc(blocks, func(b block) bool { return b.header == "factors" && !b.array })
	if i < 0 {
		return nil, errors.New("moox.toml 里没有 [factors]")
	}
	setBlockKey(&blocks[i], "enabled", value)
	var out []string
	for _, b := range blocks {
		out = append(out, b.lines...)
	}
	return []byte(strings.Join(out, "\n")), nil
}

// setHostReachability 设置 [hosts.<host>] 的 reachability：direct 表示可直达（删除该键），outbound 表示只能出站。
func setHostReachability(plain []byte, host, mode string) ([]byte, error) {
	if mode != "direct" && mode != "outbound" {
		return nil, errors.New("--set-reachability 的取值只能是 direct 或 outbound")
	}
	blocks := splitBlocks(strings.Split(string(plain), "\n"))
	i := slices.IndexFunc(blocks, func(b block) bool { return b.header == "hosts."+host && !b.array })
	if i < 0 {
		return nil, fmt.Errorf("moox.toml 里没有主机 %s", host)
	}
	if mode == "outbound" {
		setBlockKey(&blocks[i], "reachability", `"outbound"`)
	} else {
		lines := blocks[i].lines[:1]
		for _, line := range blocks[i].lines[1:] {
			if key, _, ok := keyValue(line); !ok || key != "reachability" {
				lines = append(lines, line)
			}
		}
		blocks[i].lines = lines
	}
	var out []string
	for _, b := range blocks {
		out = append(out, b.lines...)
	}
	return []byte(strings.Join(out, "\n")), nil
}

// addHostDeployments 把组件并入 [deployments] 里该主机的列表（没有就新建条目，已有的组件不重复），返回实际新增的组件。
func addHostDeployments(plain []byte, host string, components []string) ([]byte, []string, error) {
	blocks := splitBlocks(strings.Split(string(plain), "\n"))
	if !slices.ContainsFunc(blocks, func(b block) bool { return b.header == "hosts."+host && !b.array }) {
		return nil, nil, fmt.Errorf("moox.toml 里没有主机 %s", host)
	}
	i := slices.IndexFunc(blocks, func(b block) bool { return b.header == "deployments" && !b.array })
	if i < 0 {
		return nil, nil, errors.New("moox.toml 里没有 [deployments]")
	}
	var current []string
	for _, line := range blocks[i].lines[1:] {
		if key, value, ok := keyValue(line); ok && key == host {
			for _, item := range regexp.MustCompile(`"([^"]+)"`).FindAllStringSubmatch(value, -1) {
				current = append(current, item[1])
			}
		}
	}
	var added []string
	merged := slices.Clone(current)
	for _, component := range components {
		if component = strings.TrimSpace(component); component != "" && !slices.Contains(merged, component) {
			merged = append(merged, component)
			added = append(added, component)
		}
	}
	if len(added) == 0 {
		return plain, nil, nil
	}
	quoted := make([]string, len(merged))
	for n, component := range merged {
		quoted[n] = fmt.Sprintf("%q", component)
	}
	setBlockKey(&blocks[i], host, "["+strings.Join(quoted, ", ")+"]")
	var out []string
	for _, b := range blocks {
		out = append(out, b.lines...)
	}
	return []byte(strings.Join(out, "\n")), added, nil
}

// setHostTLSMode 设置 [hosts.<host>] 的 tls_mode。
func setHostTLSMode(plain []byte, host, mode string) ([]byte, error) {
	if mode != "auto" && mode != "public" && mode != "internal" {
		return nil, errors.New("--set-tls-mode 的模式只能是 auto、public 或 internal")
	}
	blocks := splitBlocks(strings.Split(string(plain), "\n"))
	i := slices.IndexFunc(blocks, func(b block) bool { return b.header == "hosts."+host && !b.array })
	if i < 0 {
		return nil, fmt.Errorf("moox.toml 里没有主机 %s", host)
	}
	setBlockKey(&blocks[i], "tls_mode", fmt.Sprintf("%q", mode))
	var out []string
	for _, b := range blocks {
		out = append(out, b.lines...)
	}
	return []byte(strings.Join(out, "\n")), nil
}
