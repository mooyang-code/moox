package command

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/mooyang-code/moox/packages/servicecatalog"
	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---- 协议解析：核对每条命令规格的请求字段确实存在于 proto ----

type protoField struct {
	Name, Type string
	Repeated   bool
}

type protoIndex struct {
	messages map[string]map[string]protoField // "包.消息名" → 字段名 → 字段
	enums    map[string]bool
	rpcs     map[string]string // "包.服务/方法" → "包.请求消息名"
}

var (
	reComment  = regexp.MustCompile(`(?s)/\*.*?\*/|//[^\n]*`)
	reMessage  = regexp.MustCompile(`\bmessage\s+(\w+)\s*\{`)
	reEnum     = regexp.MustCompile(`\benum\s+(\w+)\s*\{`)
	reService  = regexp.MustCompile(`\bservice\s+(\w+)\s*\{`)
	reRPC      = regexp.MustCompile(`rpc\s+(\w+)\s*\(\s*([\w.]+)\s*\)`)
	reField    = regexp.MustCompile(`(?m)(?:^|[;{}\s])(repeated\s+|optional\s+)?(map<[^>]+>|[A-Za-z_][\w.]*)\s+(\w+)\s*=\s*\d+`)
	rePackage  = regexp.MustCompile(`package\s+([\w.]+)\s*;`)
	reNested   = regexp.MustCompile(`\b(message|enum|oneof)\s+\w+\s*\{`)
	reScalarTy = regexp.MustCompile(`^(string|bool|bytes|double|float|u?int(32|64)|s?fixed(32|64)|sint(32|64))$`)
)

func repoRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	require.True(t, ok)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "..", ".."))
}

func matchBrace(text string, open int) int {
	depth := 1
	for i := open; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(text)
}

func loadProtoIndex(t *testing.T) *protoIndex {
	t.Helper()
	root := repoRoot(t)
	var files []string
	for _, pattern := range []string{"modules/*/proto/*.proto", "packages/*/*.proto", "packages/*/proto/*.proto"} {
		matches, err := filepath.Glob(filepath.Join(root, pattern))
		require.NoError(t, err)
		files = append(files, matches...)
	}
	require.NotEmpty(t, files, "找不到 proto 文件")
	index := &protoIndex{messages: map[string]map[string]protoField{}, enums: map[string]bool{}, rpcs: map[string]string{}}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		require.NoError(t, err)
		text := reComment.ReplaceAllString(string(raw), "")
		pkg := ""
		if m := rePackage.FindStringSubmatch(text); m != nil {
			pkg = m[1]
		}
		for _, m := range reEnum.FindAllStringSubmatch(text, -1) {
			index.enums[m[1]] = true
		}
		for _, loc := range reMessage.FindAllStringSubmatchIndex(text, -1) {
			name := text[loc[2]:loc[3]]
			body := text[loc[1]:matchBrace(text, loc[1])]
			// 去掉嵌套的 message、enum、oneof 块，只留当前消息自己的字段（oneof 里的字段保留名字）。
			for {
				n := reNested.FindStringIndex(body)
				if n == nil {
					break
				}
				end := matchBrace(body, n[1])
				inner := body[n[1]:end]
				kind := body[n[0]:n[1]]
				body = body[:n[0]] + body[min(end+1, len(body)):]
				if strings.HasPrefix(kind, "oneof") {
					body += "\n" + inner
				}
			}
			fields := map[string]protoField{}
			for _, f := range reField.FindAllStringSubmatch(body, -1) {
				// 类型保留原样（可能带 common. 之类的前缀），解析时再按包找到消息。
				fields[f[3]] = protoField{Name: f[3], Type: f[2], Repeated: strings.Contains(f[1], "repeated")}
			}
			index.messages[pkg+"."+name] = fields
		}
		for _, loc := range reService.FindAllStringSubmatchIndex(text, -1) {
			service := text[loc[2]:loc[3]]
			body := text[loc[1]:matchBrace(text, loc[1])]
			for _, r := range reRPC.FindAllStringSubmatch(body, -1) {
				index.rpcs[pkg+"."+service+"/"+r[1]] = index.qualify(pkg, r[2])
			}
		}
	}
	return index
}

// qualify 把消息类型名补成“包.消息名”：不带前缀的取当前包；带 common. 之类前缀的找末段相同的包。
// 解析 proto 时消息可能还没全部读入，所以这里只做不依赖其他文件的规则，找不到的前缀在 resolve 里再查。
func (p *protoIndex) qualify(pkg, typeName string) string {
	if !strings.Contains(typeName, ".") {
		return pkg + "." + typeName
	}
	return typeName
}

// resolve 返回类型对应的消息字段表。
func (p *protoIndex) resolve(pkg, typeName string) (string, map[string]protoField, bool) {
	if fields, ok := p.messages[p.qualify(pkg, typeName)]; ok {
		return p.qualify(pkg, typeName), fields, true
	}
	if i := strings.LastIndex(typeName, "."); i >= 0 {
		prefix, name := typeName[:i], typeName[i+1:]
		for key, fields := range p.messages {
			owner := key[:strings.LastIndex(key, ".")]
			if strings.HasSuffix(key, "."+name) && (owner == prefix || strings.HasSuffix(owner, "."+prefix)) {
				return key, fields, true
			}
		}
	}
	return "", nil, false
}

// checkPath 沿点号路径检查请求字段是否存在，返回末端字段（路径越过 map 或 Struct 时返回 ok 且字段为零值）。
func (p *protoIndex) checkPath(message, path string) (field protoField, ok bool, why string) {
	current := message
	parts := strings.Split(path, ".")
	for i, part := range parts {
		pkg := current[:strings.LastIndex(current, ".")]
		name, fields, known := p.resolve(pkg, current[strings.LastIndex(current, ".")+1:])
		if !known {
			return protoField{}, false, "找不到消息 " + current
		}
		_ = name
		f, exists := fields[part]
		if !exists {
			return protoField{}, false, "消息 " + current + " 没有字段 " + part
		}
		if i == len(parts)-1 {
			return f, true, ""
		}
		if strings.HasPrefix(f.Type, "map<") || strings.HasSuffix(f.Type, "Struct") {
			return protoField{}, true, ""
		}
		if f.Repeated {
			return protoField{}, false, "不能深入 repeated 字段 " + part
		}
		next, _, found := p.resolve(pkg, f.Type)
		if !found {
			return protoField{}, false, "找不到字段 " + part + " 的类型 " + f.Type
		}
		current = next
	}
	return protoField{}, false, "空路径"
}

func (p *protoIndex) kindMatches(kind rpcFlagKind, f protoField) (bool, string) {
	if !strings.HasPrefix(f.Type, "map<") {
		f.Type = f.Type[strings.LastIndex(f.Type, ".")+1:]
	}
	scalar := reScalarTy.MatchString(f.Type)
	isEnum := p.enums[f.Type]
	isMap := strings.HasPrefix(f.Type, "map<")
	switch kind {
	case rpcString, rpcTime, rpcFile:
		return !f.Repeated && !isMap && (f.Type == "string" || isEnum), "需要 string 或枚举，实际 " + f.Type
	case rpcInt:
		return !f.Repeated && scalar && strings.Contains(f.Type, "int"), "需要整数，实际 " + f.Type
	case rpcFloat:
		return !f.Repeated && (f.Type == "double" || f.Type == "float"), "需要浮点数，实际 " + f.Type
	case rpcBool:
		return !f.Repeated && f.Type == "bool", "需要 bool，实际 " + f.Type
	case rpcStrings:
		return f.Repeated && f.Type == "string", "需要 repeated string，实际 " + f.Type
	case rpcStringMap:
		return isMap && strings.Contains(f.Type, "string, string"), "需要 map<string, string>，实际 " + f.Type
	case rpcNumberMap:
		return isMap && (strings.Contains(f.Type, "double") || strings.Contains(f.Type, "int")), "需要数值映射，实际 " + f.Type
	}
	return true, ""
}

func splitServicePath(path string) (pkg, service string) {
	i := strings.LastIndex(path, ".")
	return path[:i], path[i+1:]
}

func TestRPCSpecsMatchProtoAndCatalog(t *testing.T) {
	index := loadProtoIndex(t)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	type grant struct{ methods map[string]bool }
	allowed := map[string]map[string]bool{} // 服务 → moox-cli 可调用的方法
	known := map[string]map[string]bool{}
	for _, component := range catalog.Components {
		for _, service := range component.Services {
			known[service.Path] = map[string]bool{}
			for _, m := range service.Methods {
				known[service.Path][m] = true
			}
			allowed[service.Path] = map[string]bool{}
			for _, acl := range service.ACL {
				if slices.Contains(acl.Callers, "moox-cli") {
					for _, m := range acl.Methods {
						allowed[service.Path][m] = true
					}
				}
			}
		}
	}
	reserved := []string{"file", "data", "yes", "space-id", "host", "page", "page-size", "help"}
	paths := map[string]bool{}
	for _, spec := range allRPCSpecs() {
		name := strings.Join(spec.Path, " ")
		t.Run(name, func(t *testing.T) {
			assert.False(t, paths[name], "命令路径重复")
			paths[name] = true
			require.True(t, known[spec.Service][spec.Method], "服务目录里没有 %s %s", spec.Service, spec.Method)
			require.True(t, allowed[spec.Service][spec.Method], "moox-cli 无权调用 %s %s（gateway methods 会标出）", spec.Service, spec.Method)
			if reason, denied := gatewayCallDenied[spec.key()]; denied {
				t.Fatalf("%s 是 gateway call 禁用的方法（%s），专用命令不应输出它", spec.key(), reason)
			}
			pkg, service := splitServicePath(spec.Service)
			request, ok := index.rpcs[pkg+"."+service+"/"+spec.Method]
			require.True(t, ok, "proto 里没有 rpc %s.%s/%s", pkg, service, spec.Method)

			seen := map[string]bool{}
			for _, flag := range spec.Flags {
				assert.False(t, seen[flag.Name], "参数 --%s 重复", flag.Name)
				seen[flag.Name] = true
				if !spec.NoSpace || (flag.Name != "space-id") {
					assert.NotContains(t, reserved, flag.Name, "--%s 是命令自带的参数名", flag.Name)
				}
				for _, path := range []string{flag.Field, flag.AlsoField} {
					if path == "" {
						continue
					}
					field, exists, why := index.checkPath(request, path)
					require.True(t, exists, "--%s → %s：%s", flag.Name, path, why)
					if field.Name != "" {
						matches, reason := index.kindMatches(flag.Kind, field)
						assert.True(t, matches, "--%s → %s：%s", flag.Name, path, reason)
					}
				}
			}
			for field := range spec.Fixed {
				_, exists, why := index.checkPath(request, field)
				assert.True(t, exists, "固定字段 %s：%s", field, why)
			}
			for _, field := range spec.AutoIDs {
				_, exists, why := index.checkPath(request, field)
				assert.True(t, exists, "自动 ID 字段 %s：%s", field, why)
			}
			if spec.SpaceField != "" {
				assert.False(t, spec.NoSpace, "NoSpace 与 SpaceField 矛盾")
				_, exists, why := index.checkPath(request, spec.SpaceField)
				assert.True(t, exists, "空间字段 %s：%s", spec.SpaceField, why)
			}
			if spec.Page != rpcNoPage {
				key := "size"
				if spec.Page == rpcPagePSize {
					key = "page_size"
				}
				for _, path := range []string{"page.page", "page." + key} {
					_, exists, why := index.checkPath(request, path)
					assert.True(t, exists, "分页字段 %s：%s", path, why)
				}
			}
		})
	}
}

func TestRPCSpecsMountIntoCommandTree(t *testing.T) {
	mountRPCCommands(rootCmd)
	for _, spec := range allRPCSpecs() {
		cmd, rest, err := rootCmd.Find(spec.Path)
		require.NoError(t, err, strings.Join(spec.Path, " "))
		require.Empty(t, rest, "%s 没有挂到命令树上", strings.Join(spec.Path, " "))
		require.Equal(t, spec.Path[len(spec.Path)-1], cmd.Name())
		require.NotEmpty(t, cmd.Short)
	}
}

// 管理台能调用的每个方法都要有专用命令，或者明确记下为什么不做命令：管理台新增接口时忘了做命令，这个测试会失败。
func TestEveryConsoleMethodHasACommandOrAReason(t *testing.T) {
	mountRPCCommands(rootCmd)
	catalog, err := servicecatalog.LoadEmbedded()
	require.NoError(t, err)
	console := map[string]bool{}
	for _, component := range catalog.Components {
		for _, service := range component.Services {
			for _, acl := range service.ACL {
				if !slices.Contains(acl.Callers, "console") {
					continue
				}
				for _, method := range acl.Methods {
					console[service.Path+"/"+method] = true
				}
			}
		}
	}
	covered := map[string]string{}
	for _, spec := range allRPCSpecs() {
		covered[spec.key()] = strings.Join(spec.Path, " ")
	}
	var missing []string
	for key := range console {
		if covered[key] != "" {
			continue
		}
		if path, ok := handWrittenCoverage[key]; ok {
			cmd, rest, err := rootCmd.Find(strings.Fields(path))
			require.NoError(t, err, key)
			assert.Empty(t, rest, "%s 对应的命令 %q 不存在", key, path)
			assert.NotNil(t, cmd)
			continue
		}
		if reason, ok := consoleOnly[key]; ok {
			assert.NotEmpty(t, reason, key)
			continue
		}
		missing = append(missing, key)
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "这些管理台接口既没有专用命令也没有说明原因：\n%s", strings.Join(missing, "\n"))
	for key := range handWrittenCoverage {
		assert.True(t, console[key], "handWrittenCoverage 里的 %s 不是管理台能调用的方法", key)
		assert.Empty(t, covered[key], "%s 已经由规格命令覆盖，不要在 handWrittenCoverage 里重复", key)
	}
	for key := range consoleOnly {
		assert.True(t, console[key], "consoleOnly 里的 %s 不是管理台能调用的方法", key)
		assert.Empty(t, covered[key], "%s 已经有专用命令，不要列为只在管理台", key)
		_, dup := handWrittenCoverage[key]
		assert.False(t, dup, "%s 同时出现在两张表里", key)
	}
}

// 一致性检查本身要能抓到错误：不存在的字段、类型对不上都必须被发现。
func TestProtoIndexCatchesWrongSpecs(t *testing.T) {
	index := loadProtoIndex(t)
	request := index.rpcs["trpc.moox.factor.FactorMgr/ListFactors"]
	require.Equal(t, "trpc.moox.factor.ListFactorsReq", request)

	field, ok, _ := index.checkPath(request, "task_id")
	require.True(t, ok)
	matches, _ := index.kindMatches(rpcString, field)
	assert.True(t, matches)
	matches, _ = index.kindMatches(rpcBool, field)
	assert.False(t, matches, "string 字段不能用布尔参数")

	_, ok, why := index.checkPath(request, "no_such_field")
	assert.False(t, ok)
	assert.Contains(t, why, "没有字段")

	// 同名消息分属不同的包：admin 的 Space 有 market，storage 的没有。
	_, ok, _ = index.checkPath(index.rpcs["trpc.moox.admin.SpaceMgr/CreateSpace"], "space.market")
	assert.True(t, ok)
	_, ok, _ = index.checkPath(index.rpcs["trpc.moox.storage.Metadata/CreateSpace"], "space.market")
	assert.False(t, ok)

	// 嵌套消息与公共包里的分页。
	_, ok, why = index.checkPath(request, "page.size")
	assert.True(t, ok, why)
	_, ok, _ = index.checkPath(index.rpcs["trpc.moox.strategy.StrategyMgr/ListStrategies"], "page.page_size")
	assert.True(t, ok)
	repeated, ok, _ := index.checkPath(index.rpcs["trpc.moox.factor.FactorMgr/RecalcFactors"], "members")
	require.True(t, ok)
	matches, _ = index.kindMatches(rpcStrings, repeated)
	assert.True(t, matches)
}

// skill 的完整命令清单必须列出每一个叶子命令：新增命令忘了写进 skills/moox/references/command-index.md，Agent 就不知道有它。
func TestSkillCommandIndexListsEveryCommand(t *testing.T) {
	mountRPCCommands(rootCmd)
	raw, err := os.ReadFile(filepath.Join(repoRoot(t), "skills", "moox", "references", "command-index.md"))
	require.NoError(t, err)
	index := string(raw)
	var leaves []string
	var walk func(cmd *cobra.Command)
	walk = func(cmd *cobra.Command) {
		children := 0
		for _, child := range cmd.Commands() {
			if child.Name() == "help" || child.Hidden {
				continue
			}
			children++
			walk(child)
		}
		if children == 0 && cmd != rootCmd {
			leaves = append(leaves, strings.TrimPrefix(cmd.CommandPath(), rootCmd.Name()+" "))
		}
	}
	walk(rootCmd)
	require.Greater(t, len(leaves), 200)
	var missing []string
	for _, leaf := range leaves {
		if !strings.Contains(index, "| `"+leaf+"` |") {
			missing = append(missing, leaf)
		}
	}
	sort.Strings(missing)
	assert.Empty(t, missing, "这些命令没有写进 skills/moox/references/command-index.md：\n%s", strings.Join(missing, "\n"))
}
