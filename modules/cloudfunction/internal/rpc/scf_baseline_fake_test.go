package rpc

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	tencentscf "github.com/mooyang-code/moox/modules/cloudfunction/internal/providers/tencentscf"
)

// baselineSCF 是一个带状态、只记录写请求的假腾讯云，给批 2 的金样用：
// 每条路径结束时导出云上的最终状态（环境、配置、定时器），并留下写请求的先后顺序。
// 它只依赖 tencentscf 的请求与返回类型，批 2 的 T4 把它连同金样文件一起搬进 Collector 的 cloud 包。
//
// 状态变化按真实客户端（tencentscf.Client）的语义模拟：
//   - UpdateFunctionConfiguration 的环境整份替换；内存、超时、并发、公网为零值时保持不变；
//     VPC 只在请求带了或要求清除时才变。
//   - EnsureTimerTrigger 没有就建、有就按请求改，读回的可用状态固定为 Available。
//   - 建函数、换代码、改配置之后，函数先停在 Creating 或 Updating，被读过 pendingReads 次后才回到 Active
//     （pendingReads 为 0 时立即 Active）。函数不是 Active 时，换代码、改配置、设定时器、预留并发、
//     异步重试这些写请求一律被拒绝：真实的腾讯云也是这样，调用方必须先等到 Active。
type baselineSCF struct {
	mu sync.Mutex
	// pendingReads 是每次建函数、换代码、改配置之后，函数回到 Active 之前要被读的次数。
	pendingReads int
	namespaces   map[string]struct{}
	functions    map[string]*baselineFunction
	// writes 是写请求的先后顺序，每项一行。只读请求（GetFunction、GetTimerTrigger）不记。
	writes []string
	// rejected 是因为函数不是 Active 而被拒绝的写请求（不计入 writes）。
	rejected []string
}

// baselineFunction 是假腾讯云里一个函数的全部状态。
type baselineFunction struct {
	info        tencentscf.FunctionInfo
	handler     string
	description string
	cosBucket   string
	cosRegion   string
	cosObject   string
	// nativeCLSCleared 记录 SCF 自带的日志投递是否已被清掉：清过一次就一直是清过，后面不带清除标记的改配置不会把它恢复。
	nativeCLSCleared bool
	// pending 是函数回到 Active 之前还要被读的次数。
	pending     int
	timer       *tencentscf.TimerTriggerInfo
	reservedMem int64
	asyncRetry  *baselineAsyncRetry
}

type baselineAsyncRetry struct {
	RetryNum int64 `json:"retry_num"`
	MsgTTL   int64 `json:"msg_ttl"`
}

// baselineFunctionState 是导出到金样文件的函数状态。
type baselineFunctionState struct {
	Status                 string              `json:"status"`
	Runtime                string              `json:"runtime"`
	Type                   string              `json:"type"`
	Handler                string              `json:"handler"`
	Description            string              `json:"description"`
	MemorySize             int64               `json:"memory_size"`
	Timeout                int64               `json:"timeout"`
	MaxInstanceConcurrency int64               `json:"max_instance_concurrency"`
	PublicNetStatus        string              `json:"public_net_status"`
	VpcID                  string              `json:"vpc_id"`
	SubnetID               string              `json:"subnet_id"`
	Code                   baselineCode        `json:"code"`
	NativeCLSCleared       bool                `json:"native_cls_cleared"`
	Environment            map[string]string   `json:"environment"`
	Timer                  *baselineTimerState `json:"timer"`
	ReservedConcurrencyMem int64               `json:"reserved_concurrency_mem"`
	AsyncRetry             *baselineAsyncRetry `json:"async_retry"`
}

type baselineCode struct {
	COSBucket string `json:"cos_bucket"`
	COSRegion string `json:"cos_region"`
	COSObject string `json:"cos_object"`
}

type baselineTimerState struct {
	Name            string `json:"name"`
	Type            string `json:"type"`
	Cron            string `json:"cron"`
	Enabled         bool   `json:"enabled"`
	AvailableStatus string `json:"available_status"`
	Qualifier       string `json:"qualifier"`
	Message         string `json:"message"`
}

// baselineState 是一条路径结束时假腾讯云的最终状态。
type baselineState struct {
	Namespaces []string                         `json:"namespaces"`
	Functions  map[string]baselineFunctionState `json:"functions"`
}

func newBaselineSCF() *baselineSCF {
	return &baselineSCF{namespaces: map[string]struct{}{}, functions: map[string]*baselineFunction{}}
}

func baselineKey(ref tencentscf.FunctionRef) string {
	return ref.Region + "/" + ref.Namespace + "/" + ref.FunctionName
}

func copyBaselineEnvironment(values map[string]string) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

// record 记一条写请求。调用方已持有锁。
func (f *baselineSCF) record(format string, args ...any) {
	f.writes = append(f.writes, fmt.Sprintf(format, args...))
}

// packageMarker 返回函数当前的代码包标记与实际代码对象，写进定时器请求的记录里，
// 用来检查“代码包落后时不启用定时器”。
func (fn *baselineFunction) packageMarker() string {
	return fmt.Sprintf("package_id=%s code=%s", fn.info.Environment["MOOX_CODE_PACKAGE_ID"], fn.cosObject)
}

// seed 直接在假腾讯云里放一个函数（不记写请求）。
func (f *baselineSCF) seed(ref tencentscf.FunctionRef, function *baselineFunction) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.namespaces[ref.Region+"/"+ref.Namespace] = struct{}{}
	f.functions[baselineKey(ref)] = function
}

// mutate 直接改假腾讯云里一个函数的状态（不记写请求），用来模拟云上的现状。
func (f *baselineSCF) mutate(ref tencentscf.FunctionRef, change func(*baselineFunction)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f.functions[baselineKey(ref)])
}

// resetWrites 清空写请求记录，开始录下一条路径。
func (f *baselineSCF) resetWrites() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.writes = nil
}

// snapshot 导出写请求顺序与最终状态。
func (f *baselineSCF) snapshot() ([]string, baselineState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	state := baselineState{Functions: map[string]baselineFunctionState{}}
	for namespace := range f.namespaces {
		state.Namespaces = append(state.Namespaces, namespace)
	}
	sort.Strings(state.Namespaces)
	for key, function := range f.functions {
		item := baselineFunctionState{
			Status: function.info.Status, Runtime: function.info.Runtime, Type: function.info.Type,
			Handler: function.handler, Description: function.description,
			MemorySize: function.info.MemorySize, Timeout: function.info.Timeout,
			MaxInstanceConcurrency: function.info.MaxInstanceConcurrency, PublicNetStatus: function.info.PublicNetStatus,
			VpcID: function.info.VpcID, SubnetID: function.info.SubnetID,
			Code:                   baselineCode{COSBucket: function.cosBucket, COSRegion: function.cosRegion, COSObject: function.cosObject},
			NativeCLSCleared:       function.nativeCLSCleared,
			Environment:            copyBaselineEnvironment(function.info.Environment),
			ReservedConcurrencyMem: function.reservedMem, AsyncRetry: function.asyncRetry,
		}
		if timer := function.timer; timer != nil {
			item.Timer = &baselineTimerState{Name: timer.Name, Type: timer.Type, Cron: timer.Cron, Enabled: timer.Enabled, AvailableStatus: timer.AvailableStatus, Qualifier: timer.Qualifier, Message: timer.Message}
		}
		state.Functions[key] = item
	}
	return append([]string(nil), f.writes...), state
}

// rejectedWrites 返回因为函数不是 Active 而被拒绝的写请求。
func (f *baselineSCF) rejectedWrites() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.rejected...)
}

func (f *baselineSCF) EnsureNamespace(_ context.Context, region, namespace string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := region + "/" + namespace
	if _, ok := f.namespaces[key]; !ok {
		f.namespaces[key] = struct{}{}
		f.record("CreateNamespace %s", key)
	}
	return nil
}

func (f *baselineSCF) GetFunction(_ context.Context, ref tencentscf.FunctionRef) (*tencentscf.FunctionInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	function, ok := f.functions[baselineKey(ref)]
	if !ok {
		return nil, errors.New("ResourceNotFound.FunctionName")
	}
	info := function.info
	info.Environment = copyBaselineEnvironment(function.info.Environment)
	if function.pending > 0 {
		// 这一次读到的还是过渡状态；读够次数之后回到 Active。
		function.pending--
		if function.pending == 0 {
			function.info.Status = "Active"
		}
	}
	return &info, nil
}

// transition 让函数进入过渡状态（调用方已持有锁）。
func (f *baselineSCF) transition(function *baselineFunction, status string) {
	if f.pendingReads > 0 {
		function.info.Status, function.pending = status, f.pendingReads
	}
}

// requireActive 在函数不是 Active 时拒绝写请求（调用方已持有锁）。
func (f *baselineSCF) requireActive(operation, key string, function *baselineFunction) error {
	if function.info.Status == "Active" {
		return nil
	}
	f.rejected = append(f.rejected, fmt.Sprintf("%s %s status=%s", operation, key, function.info.Status))
	return fmt.Errorf("FailedOperation.FunctionStatusError: %s 在函数状态为 %s 时不能执行", operation, function.info.Status)
}

func (f *baselineSCF) CreateFunction(_ context.Context, req tencentscf.CreateFunctionRequest) (*tencentscf.CreateFunctionResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := baselineKey(req.FunctionRef)
	if _, ok := f.functions[key]; ok {
		return nil, errors.New("ResourceInUse.FunctionName")
	}
	function := &baselineFunction{
		info: tencentscf.FunctionInfo{
			Status: "Active", Runtime: req.Runtime, Type: req.Type, MemorySize: req.MemorySize, Timeout: req.Timeout,
			MaxInstanceConcurrency: req.MaxInstanceConcurrency, PublicNetStatus: strings.ToUpper(strings.TrimSpace(req.PublicNetStatus)),
			VpcID: req.VpcID, SubnetID: req.SubnetID, Environment: copyBaselineEnvironment(req.Environment),
		},
		handler: req.Handler, description: req.Description,
		cosBucket: req.COSBucket, cosRegion: req.COSRegion, cosObject: req.COSObject,
	}
	f.functions[key] = function
	f.transition(function, "Creating")
	f.record("CreateFunction %s %s", key, function.packageMarker())
	return &tencentscf.CreateFunctionResponse{}, nil
}

func (f *baselineSCF) DeleteFunction(_ context.Context, ref tencentscf.FunctionRef) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := baselineKey(ref)
	if _, ok := f.functions[key]; !ok {
		return errors.New("ResourceNotFound.FunctionName")
	}
	delete(f.functions, key)
	f.record("DeleteFunction %s", key)
	return nil
}

func (f *baselineSCF) UpdateFunctionCode(_ context.Context, req tencentscf.UpdateFunctionCodeRequest) (*tencentscf.UpdateFunctionCodeResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := baselineKey(req.FunctionRef)
	function, ok := f.functions[key]
	if !ok {
		return nil, errors.New("ResourceNotFound.FunctionName")
	}
	if err := f.requireActive("UpdateFunctionCode", key, function); err != nil {
		return nil, err
	}
	function.handler, function.cosBucket, function.cosRegion, function.cosObject = req.Handler, req.COSBucket, req.COSRegion, req.COSObject
	f.transition(function, "Updating")
	f.record("UpdateFunctionCode %s %s", key, function.packageMarker())
	return &tencentscf.UpdateFunctionCodeResponse{}, nil
}

func (f *baselineSCF) UpdateFunctionConfiguration(_ context.Context, req tencentscf.UpdateFunctionConfigurationRequest) (*tencentscf.UpdateFunctionConfigurationResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := baselineKey(req.FunctionRef)
	function, ok := f.functions[key]
	if !ok {
		return nil, errors.New("ResourceNotFound.FunctionName")
	}
	if err := f.requireActive("UpdateFunctionConfiguration", key, function); err != nil {
		return nil, err
	}
	function.info.Environment = copyBaselineEnvironment(req.Environment)
	if req.MemorySize > 0 {
		function.info.MemorySize = req.MemorySize
	}
	if req.Timeout > 0 {
		function.info.Timeout = req.Timeout
	}
	if req.MaxInstanceConcurrency > 0 {
		function.info.MaxInstanceConcurrency = req.MaxInstanceConcurrency
	}
	if status := strings.ToUpper(strings.TrimSpace(req.PublicNetStatus)); status != "" {
		function.info.PublicNetStatus = status
	}
	if req.ClearVPC {
		function.info.VpcID, function.info.SubnetID = "", ""
	} else if strings.TrimSpace(req.VpcID) != "" || strings.TrimSpace(req.SubnetID) != "" {
		function.info.VpcID, function.info.SubnetID = strings.TrimSpace(req.VpcID), strings.TrimSpace(req.SubnetID)
	}
	if req.ClearNativeCLS {
		function.nativeCLSCleared = true
	}
	f.transition(function, "Updating")
	f.record("UpdateFunctionConfiguration %s %s", key, function.packageMarker())
	return &tencentscf.UpdateFunctionConfigurationResponse{}, nil
}

func (f *baselineSCF) EnsureTimerTrigger(_ context.Context, req tencentscf.TimerTriggerRequest) (*tencentscf.TimerTriggerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := baselineKey(req.FunctionRef)
	function, ok := f.functions[key]
	if !ok {
		return nil, errors.New("ResourceNotFound.FunctionName")
	}
	if err := f.requireActive("EnsureTimerTrigger", key, function); err != nil {
		return nil, err
	}
	function.timer = &tencentscf.TimerTriggerInfo{Name: req.Name, Type: "timer", Cron: req.Cron, Enabled: req.Enabled, AvailableStatus: "Available", Qualifier: req.Qualifier, Message: req.Message}
	f.record("EnsureTimerTrigger %s cron=%q enabled=%t %s", key, req.Cron, req.Enabled, function.packageMarker())
	info := *function.timer
	return &info, nil
}

func (f *baselineSCF) GetTimerTrigger(_ context.Context, ref tencentscf.FunctionRef, _ string) (*tencentscf.TimerTriggerInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	function, ok := f.functions[baselineKey(ref)]
	if !ok || function.timer == nil {
		return nil, errors.New("ResourceNotFound.Trigger")
	}
	info := *function.timer
	return &info, nil
}

func (f *baselineSCF) DeleteTimerTrigger(_ context.Context, req tencentscf.TimerTriggerRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := baselineKey(req.FunctionRef)
	function, ok := f.functions[key]
	if !ok || function.timer == nil {
		return errors.New("ResourceNotFound.Trigger")
	}
	function.timer = nil
	f.record("DeleteTimerTrigger %s", key)
	return nil
}

func (f *baselineSCF) InvokeFunction(context.Context, tencentscf.InvokeFunctionRequest) (*tencentscf.InvokeFunctionResponse, error) {
	return &tencentscf.InvokeFunctionResponse{}, nil
}

// PutReservedConcurrencyConfig 是靠类型断言调用的可选方法（预留并发）。
func (f *baselineSCF) PutReservedConcurrencyConfig(_ context.Context, req tencentscf.PutReservedConcurrencyConfigRequest) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := baselineKey(req.FunctionRef)
	function, ok := f.functions[key]
	if !ok {
		return errors.New("ResourceNotFound.FunctionName")
	}
	if err := f.requireActive("PutReservedConcurrencyConfig", key, function); err != nil {
		return err
	}
	function.reservedMem = req.ReservedConcurrencyMem
	f.record("PutReservedConcurrencyConfig %s mem=%d", key, req.ReservedConcurrencyMem)
	return nil
}

// UpdateFunctionEventInvokeConfig 是靠类型断言调用的可选方法（异步重试）。
func (f *baselineSCF) UpdateFunctionEventInvokeConfig(_ context.Context, req tencentscf.UpdateFunctionEventInvokeConfigRequest) (*tencentscf.UpdateFunctionEventInvokeConfigResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	key := baselineKey(req.FunctionRef)
	function, ok := f.functions[key]
	if !ok {
		return nil, errors.New("ResourceNotFound.FunctionName")
	}
	if err := f.requireActive("UpdateFunctionEventInvokeConfig", key, function); err != nil {
		return nil, err
	}
	function.asyncRetry = &baselineAsyncRetry{RetryNum: req.RetryNum, MsgTTL: req.MsgTTL}
	f.record("UpdateFunctionEventInvokeConfig %s retry=%d msg_ttl=%d", key, req.RetryNum, req.MsgTTL)
	return &tencentscf.UpdateFunctionEventInvokeConfigResponse{}, nil
}
