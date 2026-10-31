package command

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mooyang-code/moox/modules/cli/internal/adminclient"
	"github.com/spf13/cobra"
)

// 本文件是“管理接口专用命令”的通用实现：每个命令是一条 rpcSpec（服务、方法、参数到请求字段的映射），
// 由 buildRPCCommand 生成 cobra 命令。统一处理 --file、--data、--space-id、--host、--yes、分页、
// ret_info 判定与退出码，各模块的命令只需要写规格表（见 rpc_specs_*.go）。

// gatewayCallDenied 是 gateway call 拒绝的方法：它们的响应含明文密钥或完整 webhook，
// 不能经由通用入口流入 Agent 的对话。专用命令在进程内使用这些值，不会打印。
const secretWriteDenied = "密钥的新建、修改、启停与删除由用户在管理台“设置 › 密钥”操作；明文不经过 Agent，误删也无法恢复"

var gatewayCallDenied = map[string]string{
	"trpc.moox.ops.SecretMgr/GetSecretValue":                "返回密钥明文；云账户等凭证由专用命令在进程内使用，需要查看请到管理台“设置 › 密钥”",
	"trpc.moox.monitor.MonitorMgr/ListNotificationChannels": "返回完整 webhook 地址；查看告警渠道用 moox-cli config notification（地址已打码）",
	"trpc.moox.ops.SecretMgr/CreateSecret":                  secretWriteDenied,
	"trpc.moox.ops.SecretMgr/UpdateSecret":                  secretWriteDenied,
	"trpc.moox.ops.SecretMgr/ToggleSecretStatus":            secretWriteDenied,
	"trpc.moox.ops.SecretMgr/DeleteSecret":                  secretWriteDenied,
}

// rpcBusinessError 是服务返回了业务失败（ret_info.code 非成功）。响应已经打印，这里只负责非 0 退出。
type rpcBusinessError struct {
	Code string
	Msg  string
}

func (e *rpcBusinessError) Error() string {
	return fmt.Sprintf("请求失败: %s (code %s)", e.Msg, e.Code)
}

// rpcRetError 读取响应顶层的 ret_info：code 为 0、"0"、"SUCCESS" 或缺省时视为成功；没有 ret_info 的响应（如 HostAgentMgr）也算成功。
func rpcRetError(raw []byte) error {
	var head struct {
		RetInfo *struct {
			Code json.RawMessage `json:"code"`
			Msg  string          `json:"msg"`
		} `json:"ret_info"`
	}
	if json.Unmarshal(raw, &head) != nil || head.RetInfo == nil {
		return nil
	}
	code := strings.Trim(strings.TrimSpace(string(head.RetInfo.Code)), `"`)
	switch code {
	case "", "0", "null", "SUCCESS":
		return nil
	}
	return &rpcBusinessError{Code: code, Msg: head.RetInfo.Msg}
}

// rpcInvocation 描述一次经网关的调用。
type rpcInvocation struct {
	File, Service, Method, SpaceID, HostID string
	Request                                any
}

// invokeRPC 打开操作员网关发起调用，返回原始响应（不判定 ret_info）。
func invokeRPC(ctx context.Context, inv rpcInvocation) (json.RawMessage, error) {
	gateway, err := openCommandGateway(ctx, inv.File, nil)
	if err != nil {
		return nil, err
	}
	defer gateway.Close()
	client := &adminclient.Client{Gateway: gateway, SpaceID: inv.SpaceID, HostID: inv.HostID}
	var response json.RawMessage
	if err := client.CallGatewayJSON(ctx, inv.Service, inv.Method, inv.Request, &response); err != nil {
		return nil, err
	}
	return response, nil
}

// runRPC 发起调用、打印响应，业务失败时先打印再返回错误（退出码非 0）。indent 为 true 时缩进输出。
func runRPC(ctx context.Context, out io.Writer, inv rpcInvocation, indent bool) error {
	response, err := invokeRPC(ctx, inv)
	if err != nil {
		return err
	}
	if err := printRPCResponse(out, response, indent); err != nil {
		return err
	}
	return rpcRetError(response)
}

func printRPCResponse(out io.Writer, response []byte, indent bool) error {
	if indent {
		var buffer bytes.Buffer
		if err := json.Indent(&buffer, response, "", "  "); err == nil {
			response = buffer.Bytes()
		}
	}
	_, err := fmt.Fprintln(out, strings.TrimRight(string(response), "\n"))
	return err
}

// ---- 规格 ----

type rpcFlagKind int

const (
	rpcString    rpcFlagKind = iota
	rpcInt                   // 整数
	rpcFloat                 // 浮点数
	rpcBool                  // 布尔；只在显式传入时写入请求
	rpcStrings               // 可重复或逗号分隔的字符串列表
	rpcTime                  // RFC3339 时间，按字符串写入
	rpcJSON                  // JSON 文本，或 @文件
	rpcFile                  // 文件路径（- 为标准输入），内容按字符串写入
	rpcStringMap             // k=v，可重复，按字符串映射写入
	rpcNumberMap             // k=数字，可重复，按数值映射写入
)

type rpcFlag struct {
	Name  string // 参数名（不含 --）；不能用 file、data、yes、space-id、host
	Field string // 请求字段，点号表示嵌套，如 "factor_task.name"
	// AlsoField 非空时同一个值再写进另一个请求字段（例如任务 ID 既在顶层又在 task 对象里）。
	AlsoField string
	Kind      rpcFlagKind
	Usage     string
	Required  bool   // 请求里没有该字段（--data 也没给）时报错
	Default   string // 只用于字符串参数：非空时未显式传入也写入（并作为帮助里的默认值）
}

type rpcPageStyle int

const (
	rpcNoPage    rpcPageStyle = iota
	rpcPageSize               // {"page":{"page":N,"size":M}}，因子、采集、存储、归档等
	rpcPagePSize              // {"page":{"page":N,"page_size":M}}，策略
)

type rpcSpec struct {
	Path         []string // 命令路径，如 {"factor", "task", "list"}
	Short        string
	Service      string // 完整服务名，如 trpc.moox.factor.FactorMgr
	Method       string
	Flags        []rpcFlag
	Page         rpcPageStyle
	SpaceField   string   // 非空时 --space-id 也写入这个请求字段（通常是 space_id）
	NoSpace      bool     // 空间无关的方法不加 --space-id
	Host         bool     // 加 --host，指定调用哪台主机上的服务
	HostRequired bool     // --host 必填：服务部署在多台主机上、不指定会静默落到别的主机时用
	Destructive  bool     // 默认只预览，--yes 才执行
	AutoIDs      []string // 请求里缺省时自动生成随机 ID 的字段
	// Fixed 是固定写入请求的字段（如启用/停用命令里的 status）。
	Fixed map[string]any
	// Merge 非空时，在发请求（和预览）之前先取回现有对象，把用户给的字段合并进去再提交：
	// 服务端的修改接口是整体替换，只给几个字段会把其余字段清空。
	Merge rpcMerge
}

// rpcMerge 读取现有对象并把用户给的字段合并进请求。request 是已经构造好的请求，实现里原地改写。
type rpcMerge func(ctx context.Context, inv rpcInvocation, request map[string]any) error

// readOnlyObjectFields 是取回的对象里永远不回传的只读字段。
var readOnlyObjectFields = map[string]bool{"id": true, "created_at": true, "updated_at": true, "create_time": true, "modify_time": true}

// mergeExisting 返回一个合并函数：fetch 取回现有对象，只保留 keep 里的字段（keep 为空表示除只读字段外全部保留），
// 用户在 request[into] 里给出的字段覆盖它，结果写回 request[into]。
func mergeExisting(into string, keep []string, fetch func(ctx context.Context, inv rpcInvocation, request map[string]any) (map[string]any, error)) rpcMerge {
	return func(ctx context.Context, inv rpcInvocation, request map[string]any) error {
		existing, err := fetch(ctx, inv, request)
		if err != nil {
			return fmt.Errorf("取回现有对象失败：%w", err)
		}
		merged := map[string]any{}
		if len(keep) == 0 {
			for key, value := range existing {
				if !readOnlyObjectFields[key] {
					merged[key] = value
				}
			}
		}
		for _, key := range keep {
			if value, ok := existing[key]; ok {
				merged[key] = value
			}
		}
		given, _ := request[into].(map[string]any)
		for key, value := range given {
			merged[key] = value
		}
		request[into] = merged
		return nil
	}
}

// mergeViaGet 用同一个服务的 Get 类方法取回现有对象：getKeys 是“取回请求的字段 ← 本命令请求里的字段路径”，
// from 是响应里对象的路径，into 是合并回提交请求的路径。
func mergeViaGet(method string, getKeys map[string]string, from, into string, keep ...string) rpcMerge {
	return mergeExisting(into, keep, func(ctx context.Context, inv rpcInvocation, request map[string]any) (map[string]any, error) {
		get := map[string]any{}
		for field, source := range getKeys {
			if value, ok := lookupRequest(request, source); ok {
				get[field] = value
			}
		}
		return fetchObject(ctx, inv, method, get, from)
	})
}

// fetchObject 调用只读方法，检查 ret_info 后取出响应里 from 路径上的对象。
func fetchObject(ctx context.Context, inv rpcInvocation, method string, getRequest map[string]any, from string) (map[string]any, error) {
	read := inv
	read.Method, read.Request = method, getRequest
	response, err := invokeRPC(ctx, read)
	if err != nil {
		return nil, err
	}
	if err := rpcRetError(response); err != nil {
		return nil, err
	}
	var decoded map[string]any
	if err := json.Unmarshal(response, &decoded); err != nil {
		return nil, err
	}
	object, _ := decoded[from].(map[string]any)
	if object == nil {
		return nil, fmt.Errorf("响应里没有 %s", from)
	}
	return object, nil
}

func (s rpcSpec) key() string { return s.Service + "/" + s.Method }

// ---- 请求构造 ----

func setRequestPath(request map[string]any, path string, value any) {
	parts := strings.Split(path, ".")
	for _, part := range parts[:len(parts)-1] {
		next, ok := request[part].(map[string]any)
		if !ok {
			next = map[string]any{}
			request[part] = next
		}
		request = next
	}
	request[parts[len(parts)-1]] = value
}

func lookupRequest(request map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	current := request
	for _, part := range parts[:len(parts)-1] {
		next, ok := current[part].(map[string]any)
		if !ok {
			return nil, false
		}
		current = next
	}
	value, ok := current[parts[len(parts)-1]]
	return value, ok
}

func lookupRequestString(request map[string]any, path string) (string, bool) {
	value, ok := lookupRequest(request, path)
	text, isString := value.(string)
	return text, ok && isString && text != ""
}

func hasRequestPath(request map[string]any, path string) bool {
	parts := strings.Split(path, ".")
	for _, part := range parts[:len(parts)-1] {
		next, ok := request[part].(map[string]any)
		if !ok {
			return false
		}
		request = next
	}
	_, ok := request[parts[len(parts)-1]]
	return ok
}

// readRPCData 读取 --data：JSON 文本、@文件 或 -（标准输入）；为空时返回空对象。
func readRPCData(data string, stdin io.Reader) (map[string]any, error) {
	if strings.TrimSpace(data) == "" {
		return map[string]any{}, nil
	}
	var raw []byte
	switch {
	case data == "-":
		var err error
		if raw, err = io.ReadAll(stdin); err != nil {
			return nil, err
		}
	case strings.HasPrefix(data, "@"):
		var err error
		if raw, err = os.ReadFile(strings.TrimPrefix(data, "@")); err != nil {
			return nil, err
		}
	default:
		raw = []byte(data)
	}
	request := map[string]any{}
	if err := json.Unmarshal(raw, &request); err != nil {
		return nil, fmt.Errorf("--data 不是合法的 JSON 对象: %w", err)
	}
	return request, nil
}

func newRPCID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

// flagValue 取出一个参数对应的请求值；未显式传入且没有默认值时 ok 为 false。
func (f rpcFlag) value(cmd *cobra.Command, stdin io.Reader) (value any, ok bool, err error) {
	flags := cmd.Flags()
	if !flags.Changed(f.Name) && f.Default == "" {
		return nil, false, nil
	}
	switch f.Kind {
	case rpcString:
		v, _ := flags.GetString(f.Name)
		return v, true, nil
	case rpcInt:
		v, _ := flags.GetInt64(f.Name)
		return v, true, nil
	case rpcFloat:
		v, _ := flags.GetFloat64(f.Name)
		return v, true, nil
	case rpcBool:
		v, _ := flags.GetBool(f.Name)
		return v, true, nil
	case rpcStrings:
		v, _ := flags.GetStringSlice(f.Name)
		return v, true, nil
	case rpcTime:
		v, _ := flags.GetString(f.Name)
		if _, err := time.Parse(time.RFC3339, v); err != nil {
			return nil, false, fmt.Errorf("--%s 需要 RFC3339 时间，例如 2026-10-01T00:00:00Z", f.Name)
		}
		return v, true, nil
	case rpcJSON:
		v, _ := flags.GetString(f.Name)
		raw := []byte(v)
		if strings.HasPrefix(v, "@") {
			if raw, err = os.ReadFile(strings.TrimPrefix(v, "@")); err != nil {
				return nil, false, err
			}
		}
		var decoded any
		if err := json.Unmarshal(raw, &decoded); err != nil {
			return nil, false, fmt.Errorf("--%s 不是合法的 JSON: %w", f.Name, err)
		}
		return decoded, true, nil
	case rpcFile:
		v, _ := flags.GetString(f.Name)
		var raw []byte
		if v == "-" {
			raw, err = io.ReadAll(stdin)
		} else {
			raw, err = os.ReadFile(v)
		}
		if err != nil {
			return nil, false, err
		}
		return string(raw), true, nil
	case rpcStringMap:
		v, _ := flags.GetStringToString(f.Name)
		out := make(map[string]any, len(v))
		for key, item := range v {
			out[key] = item
		}
		return out, true, nil
	case rpcNumberMap:
		v, _ := flags.GetStringToString(f.Name)
		out := make(map[string]any, len(v))
		for key, item := range v {
			var number json.Number
			if err := json.Unmarshal([]byte(item), &number); err != nil {
				return nil, false, fmt.Errorf("--%s 的 %s 需要数字，收到 %q", f.Name, key, item)
			}
			out[key] = number
		}
		return out, true, nil
	}
	return nil, false, fmt.Errorf("不支持的参数类型 %d", f.Kind)
}

func (f rpcFlag) register(cmd *cobra.Command) {
	switch f.Kind {
	case rpcInt:
		cmd.Flags().Int64(f.Name, 0, f.Usage)
	case rpcFloat:
		cmd.Flags().Float64(f.Name, 0, f.Usage)
	case rpcBool:
		cmd.Flags().Bool(f.Name, false, f.Usage)
	case rpcStrings:
		cmd.Flags().StringSlice(f.Name, nil, f.Usage)
	case rpcStringMap, rpcNumberMap:
		cmd.Flags().StringToString(f.Name, nil, f.Usage)
	default:
		cmd.Flags().String(f.Name, f.Default, f.Usage)
	}
}

// buildRequest 把 --data 与各参数合并成请求体：参数优先于 --data。
func (s rpcSpec) buildRequest(cmd *cobra.Command, stdin io.Reader) (map[string]any, error) {
	data, _ := cmd.Flags().GetString("data")
	request, err := readRPCData(data, stdin)
	if err != nil {
		return nil, err
	}
	// 默认值（空间 crypto、分页 1/50）不能覆盖 --data 里显式给的值；用户显式传了参数才覆盖。
	if !s.NoSpace && s.SpaceField != "" {
		space, _ := cmd.Flags().GetString("space-id")
		if space != "" && (cmd.Flags().Changed("space-id") || !hasRequestPath(request, s.SpaceField)) {
			setRequestPath(request, s.SpaceField, space)
		}
	}
	for _, flag := range s.Flags {
		value, ok, err := flag.value(cmd, stdin)
		if err != nil {
			return nil, err
		}
		if ok {
			setRequestPath(request, flag.Field, value)
			if flag.AlsoField != "" {
				setRequestPath(request, flag.AlsoField, value)
			}
		}
	}
	for field, value := range s.Fixed {
		setRequestPath(request, field, value)
	}
	switch s.Page {
	case rpcPageSize, rpcPagePSize:
		page, _ := cmd.Flags().GetInt64("page")
		size, _ := cmd.Flags().GetInt64("page-size")
		key := "size"
		if s.Page == rpcPagePSize {
			key = "page_size"
		}
		if cmd.Flags().Changed("page") || !hasRequestPath(request, "page.page") {
			setRequestPath(request, "page.page", page)
		}
		if cmd.Flags().Changed("page-size") || !hasRequestPath(request, "page."+key) {
			setRequestPath(request, "page."+key, size)
		}
	}
	for _, field := range s.AutoIDs {
		if !hasRequestPath(request, field) {
			id, err := newRPCID()
			if err != nil {
				return nil, err
			}
			setRequestPath(request, field, id)
		}
	}
	for _, flag := range s.Flags {
		if flag.Required && !hasRequestPath(request, flag.Field) {
			return nil, fmt.Errorf("缺少 --%s（也可以在 --data 里提供 %s）", flag.Name, flag.Field)
		}
	}
	return request, nil
}

// buildRPCCommand 由规格生成命令。
func buildRPCCommand(spec rpcSpec) *cobra.Command {
	kind := "只读"
	if spec.Destructive {
		kind = "破坏性，默认只预览"
	}
	cmd := &cobra.Command{
		Use:   spec.Path[len(spec.Path)-1],
		Short: spec.Short,
		Long:  fmt.Sprintf("%s\n\n对应接口：%s %s（%s）。--data 可以提供完整请求 JSON，各参数优先于它。", spec.Short, spec.Service, spec.Method, kind),
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			request, err := spec.buildRequest(cmd, cmd.InOrStdin())
			if err != nil {
				return err
			}
			file, _ := cmd.Flags().GetString("file")
			inv := rpcInvocation{File: file, Service: spec.Service, Method: spec.Method, Request: request}
			if !spec.NoSpace {
				inv.SpaceID, _ = cmd.Flags().GetString("space-id")
				// 没显式给 --space-id 时，--data 里的空间就是请求所属的空间。
				if !cmd.Flags().Changed("space-id") && spec.SpaceField != "" {
					if fromData, ok := lookupRequestString(request, spec.SpaceField); ok {
						inv.SpaceID = fromData
					}
				}
			}
			if spec.Host {
				inv.HostID, _ = cmd.Flags().GetString("host")
				if spec.HostRequired && inv.HostID == "" {
					return errors.New("缺少 --host：这个服务部署在多台主机上，要指定查哪一台（moox.toml 里的主机名）")
				}
			}
			if spec.Merge != nil {
				if err := spec.Merge(cmd.Context(), inv, request); err != nil {
					return err
				}
				inv.Request = request
			}
			if spec.Destructive {
				if yes, _ := cmd.Flags().GetBool("yes"); !yes {
					return printRPCPreview(cmd, spec, inv, request)
				}
			}
			cmd.SilenceUsage = true
			return runRPC(cmd.Context(), cmd.OutOrStdout(), inv, true)
		},
	}
	cmd.Flags().String("file", "./moox.toml", "部署配置文件；用于解析操作员 SSH 网关")
	cmd.Flags().String("data", "", "完整请求 JSON；@文件 读取文件，- 读取标准输入。各参数优先于它")
	if !spec.NoSpace {
		cmd.Flags().String("space-id", "crypto", "空间 ID")
	}
	if spec.Host {
		cmd.Flags().String("host", "", "主机 ID：只调用这台主机上的服务（moox.toml 里的主机名）")
	}
	if spec.Page != rpcNoPage {
		cmd.Flags().Int64("page", 1, "页号，从 1 开始")
		cmd.Flags().Int64("page-size", 50, "每页条数")
	}
	if spec.Destructive {
		cmd.Flags().Bool("yes", false, "确认执行；不加只预览将要发出的请求")
	}
	for _, flag := range spec.Flags {
		flag.register(cmd)
	}
	return cmd
}

func printRPCPreview(cmd *cobra.Command, spec rpcSpec, inv rpcInvocation, request map[string]any) error {
	preview := map[string]any{"service": spec.Service, "method": spec.Method, "request": request}
	if inv.SpaceID != "" {
		preview["space_id"] = inv.SpaceID
	}
	if inv.HostID != "" {
		preview["host"] = inv.HostID
	}
	return writePreview(cmd, preview)
}

// writePreview 是所有“默认只预览、--yes 才执行”命令共用的预览输出：标准输出是带 preview:true 的 JSON，
// 提示写标准错误。预览本身成功（退出码 0），Agent 据此把内容给用户确认后再加 --yes。
func writePreview(cmd *cobra.Command, details map[string]any) error {
	out := map[string]any{"preview": true}
	for key, value := range details {
		out[key] = value
	}
	out["next"] = "这是预览，没有执行；确认无误后在命令末尾加 --yes"
	fmt.Fprintln(cmd.ErrOrStderr(), "预览：未执行。确认后加 --yes。")
	return printJSON(cmd.OutOrStdout(), out)
}

// ---- 注册 ----

// rpcGroupShort 是命令组的说明；新建的组必须在这里有说明。
var rpcGroupShort = map[string]string{}

var (
	rpcSpecsMu sync.Mutex
	rpcSpecs   []rpcSpec
)

// registerRPCSpecs 在包初始化时登记规格；命令树在 mountRPCCommands 时才生成，避免依赖各文件 init 的先后。
func registerRPCSpecs(specs ...rpcSpec) {
	rpcSpecsMu.Lock()
	defer rpcSpecsMu.Unlock()
	rpcSpecs = append(rpcSpecs, specs...)
}

func allRPCSpecs() []rpcSpec {
	rpcSpecsMu.Lock()
	defer rpcSpecsMu.Unlock()
	return append([]rpcSpec(nil), rpcSpecs...)
}

var mountRPCOnce sync.Once

// mountRPCCommands 把所有登记过的规格挂到命令树上；已存在的组复用，不存在的新建。
func mountRPCCommands(root *cobra.Command) {
	mountRPCOnce.Do(func() {
		for _, spec := range allRPCSpecs() {
			parent := root
			for i, name := range spec.Path[:len(spec.Path)-1] {
				parent = findOrAddGroup(parent, name, strings.Join(spec.Path[:i+1], " "))
			}
			if existing := findChild(parent, spec.Path[len(spec.Path)-1]); existing != nil {
				panic(fmt.Sprintf("命令 %s 已存在，规格 %s 不能再注册", strings.Join(spec.Path, " "), spec.key()))
			}
			parent.AddCommand(buildRPCCommand(spec))
		}
	})
}

func findChild(parent *cobra.Command, name string) *cobra.Command {
	for _, child := range parent.Commands() {
		if child.Name() == name {
			return child
		}
	}
	return nil
}

func findOrAddGroup(parent *cobra.Command, name, path string) *cobra.Command {
	if child := findChild(parent, name); child != nil {
		return child
	}
	short := rpcGroupShort[path]
	if short == "" {
		panic("命令组 " + path + " 缺少说明（rpcGroupShort）")
	}
	group := &cobra.Command{Use: name, Short: short}
	parent.AddCommand(group)
	return group
}

// ---- 规格里常用的参数写法 ----

func strFlag(name, field, usage string) rpcFlag {
	return rpcFlag{Name: name, Field: field, Kind: rpcString, Usage: usage}
}
func reqFlag(name, field, usage string) rpcFlag {
	return rpcFlag{Name: name, Field: field, Kind: rpcString, Usage: usage, Required: true}
}
func boolFlag(name, field, usage string) rpcFlag {
	return rpcFlag{Name: name, Field: field, Kind: rpcBool, Usage: usage}
}
func intFlag(name, field, usage string) rpcFlag {
	return rpcFlag{Name: name, Field: field, Kind: rpcInt, Usage: usage}
}
func listFlag(name, field, usage string) rpcFlag {
	return rpcFlag{Name: name, Field: field, Kind: rpcStrings, Usage: usage}
}
func jsonFlag(name, field, usage string) rpcFlag {
	return rpcFlag{Name: name, Field: field, Kind: rpcJSON, Usage: usage}
}
func fileFlag(name, field, usage string, required bool) rpcFlag {
	return rpcFlag{Name: name, Field: field, Kind: rpcFile, Usage: usage, Required: required}
}
func timeFlag(name, field, usage string, required bool) rpcFlag {
	return rpcFlag{Name: name, Field: field, Kind: rpcTime, Usage: usage, Required: required}
}
func mapFlag(name, field, usage string) rpcFlag {
	return rpcFlag{Name: name, Field: field, Kind: rpcStringMap, Usage: usage}
}
