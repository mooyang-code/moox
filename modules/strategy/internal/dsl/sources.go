package dsl

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	frequencypkg "github.com/mooyang-code/moox/packages/frequency"
	"gopkg.in/yaml.v3"
)

// 本文件是附加列来源（S7）：策略只有一个主帧，sources 只增加“列的来源”。规则用 来源名.列名 读附加来源的列，
// 每根主帧读附加来源里收盘时刻不晚于这根主帧收盘时刻的最新一行（as-of），规则状态、持有计数、止损、冻结、缺数都只按主帧算。

// MaxSources 是附加来源的个数上限。
const MaxSources = 2

// sourceNamePattern 是来源名的写法：小写字母开头的 snake_case。
var sourceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)

// reservedSourceNames 是表达式里已有含义的标识符，不能当来源名。
var reservedSourceNames = map[string]struct{}{
	identifierBars: {}, identifierScore: {}, identifierInstrument: {},
	normalizerRank: {}, normalizerZScore: {}, paramFunction: {},
}

// Source 是一个附加列来源：实例另外绑定一个周期为 Bar 的因子任务，规则用 Name.列名 读它的列。
type Source struct {
	Name string
	Bar  string
}

// SourceColumn 返回来源列在帧里的名字：来源名.列名。整个管线（编译、缺数划分、冻结、解释明细）都用这个名字。
func SourceColumn(source, column string) string { return source + "." + column }

// SplitSourceColumn 把帧里的列名拆成来源名与列名；主帧的列返回空来源名。
func SplitSourceColumn(name string) (source, column string) {
	if i := strings.IndexByte(name, '.'); i > 0 {
		return name[:i], name[i+1:]
	}
	return "", name
}

// SourceNames 返回声明的来源名（声明顺序）。
func (s Strategy) SourceNames() []string {
	names := make([]string, 0, len(s.Sources))
	for _, source := range s.Sources {
		names = append(names, source.Name)
	}
	return names
}

// HasSources 报告策略声明了附加来源。
func (s Strategy) HasSources() bool { return len(s.Sources) > 0 }

// sourceSet 返回声明的来源名集合；没有来源时为 nil（表达式里的属性访问一律拒绝）。
func (s Strategy) sourceSet() map[string]struct{} {
	if len(s.Sources) == 0 {
		return nil
	}
	set := make(map[string]struct{}, len(s.Sources))
	for _, source := range s.Sources {
		set[source.Name] = struct{}{}
	}
	return set
}

func decodeSources(node *yaml.Node) ([]Source, error) {
	fields, err := mappingFields(node, "sources")
	if err != nil {
		return nil, err
	}
	sources := make([]Source, 0, len(fields))
	for _, f := range fields {
		path := "sources." + f.key
		items, err := mappingFields(f.value, path)
		if err != nil {
			return nil, err
		}
		source := Source{Name: f.key}
		for _, item := range items {
			switch item.key {
			case "bar":
				source.Bar, err = scalarRequiredText(item.value, path+".bar")
			default:
				return nil, fmt.Errorf("%s 包含未知字段 %q", path, item.key)
			}
			if err != nil {
				return nil, err
			}
		}
		sources = append(sources, source)
	}
	return sources, nil
}

// validateSources 校验 sources 的写法：至多两个，来源名是 snake_case 且不与表达式的保留标识符重名，每个来源都写了受支持的 bar；
// 主帧写了 bar 时，来源的周期要长于主帧（整数倍与日历的要求在启用时按 View 的实际周期核对）。
func validateSources(s *Strategy) error {
	if len(s.Sources) == 0 {
		return nil
	}
	if len(s.Sources) > MaxSources {
		return fmt.Errorf("sources 至多 %d 个，实际 %d 个", MaxSources, len(s.Sources))
	}
	for i := range s.Sources {
		source := &s.Sources[i]
		if !sourceNamePattern.MatchString(source.Name) {
			return fmt.Errorf("来源名 %q 必须是小写字母开头的 snake_case（至多 32 个字符）", source.Name)
		}
		if _, reserved := reservedSourceNames[source.Name]; reserved {
			return fmt.Errorf("来源名 %q 是表达式的保留标识符，请换一个名字", source.Name)
		}
		if source.Bar == "" {
			return fmt.Errorf("sources.%s 缺少 bar", source.Name)
		}
		bar, err := frequencypkg.Parse(source.Bar)
		if err != nil || !supportedBar(bar) {
			return fmt.Errorf("sources.%s 的 bar %q 不受支持，可选：%s", source.Name, source.Bar, strings.Join(supportedBars(), "、"))
		}
		source.Bar = string(bar)
		if s.Bar != "" {
			main, _ := frequencypkg.Parse(s.Bar)
			if bar.Duration() <= main.Duration() {
				return fmt.Errorf("sources.%s 的 bar %s 必须长于策略的 bar %s：附加来源只用来读更慢频率的列", source.Name, source.Bar, s.Bar)
			}
		}
	}
	return nil
}

// validateSourcesReferenced 拒绝没有被任何表达式引用的来源：没用到的来源仍要绑定任务，收盘时刻对齐的那一根还会等它的事件。
func validateSourcesReferenced(s *Strategy) error {
	if len(s.Sources) == 0 {
		return nil
	}
	referenced, err := ReferencedColumns(*s)
	if err != nil {
		return err
	}
	used := ColumnsBySource(referenced)
	for _, source := range s.Sources {
		if len(used[source.Name]) == 0 {
			return fmt.Errorf("来源 %s 没有被任何表达式引用（写成 %s.列名），请删掉它或在规则里使用它的列", source.Name, source.Name)
		}
	}
	return nil
}

// ColumnsBySource 把帧里的列名按来源分组：键为空串的是主帧的列，其余键是来源名，值是去掉来源前缀的列名（排序）。
func ColumnsBySource(columns []string) map[string][]string {
	grouped := make(map[string]map[string]struct{})
	for _, name := range columns {
		source, column := SplitSourceColumn(name)
		if grouped[source] == nil {
			grouped[source] = make(map[string]struct{})
		}
		grouped[source][column] = struct{}{}
	}
	out := make(map[string][]string, len(grouped))
	for source, set := range grouped {
		out[source] = sortedKeys(set)
	}
	return out
}

// checkSourceColumnClash 拒绝来源名与主帧的列同名：表达式里裸写的 daily 与 daily.close 会读到不同的东西，容易写错。
func checkSourceColumnClash(strategy Strategy, available map[string]struct{}) error {
	names := strategy.SourceNames()
	sort.Strings(names)
	for _, name := range names {
		if _, clash := available[name]; clash {
			return fmt.Errorf("来源名 %q 与绑定 View 的列同名，请换一个来源名", name)
		}
	}
	return nil
}

// errUndeclaredSource 是表达式里 x.y 的 x 不是已声明来源时的错误。
func errUndeclaredSource(name string) error {
	return errors.New("不允许使用属性访问，只能引用列名、bars[0].列名、bars[-1].列名，或已在 sources 声明的 来源名.列名（" + name + " 不是已声明的来源）")
}

// TaskKey 返回实例登记因子任务用的键：主帧与参考市场的任务就是市场，附加来源的任务是 来源名:市场。
func TaskKey(source, market string) string {
	if source == "" {
		return market
	}
	return source + ":" + market
}

// SplitTaskKey 把任务键拆成来源名与市场；主帧与参考市场的键返回空来源名。
func SplitTaskKey(key string) (source, market string) {
	if i := strings.IndexByte(key, ':'); i >= 0 {
		return key[:i], key[i+1:]
	}
	return "", key
}

// ValidSourceName 报告来源名的写法合法：小写字母开头的 snake_case，且不是表达式的保留标识符。
func ValidSourceName(name string) bool {
	_, reserved := reservedSourceNames[name]
	return sourceNamePattern.MatchString(name) && !reserved
}
