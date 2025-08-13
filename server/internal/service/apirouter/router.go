// Package apirouter 封装标准的api接口（兼容wuji协议，方便业务使用wuji缓存本api结果），相当于是wuji接口的接入层
package apirouter

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"trpc.group/trpc-go/trpc-go/errs"
	thttp "trpc.group/trpc-go/trpc-go/http"
	"trpc.group/trpc-go/trpc-go/log"
	"trpc.group/trpc-go/trpc-go/server"

	"github.com/bitly/go-simplejson"
	"github.com/gorilla/mux"
	jsoniter "github.com/json-iterator/go"
)

var (
	apiHandleInstance *APIHandle
	apiHandleOnce     sync.Once
)

// SchemaHandler HTTP接口读写处理器
type SchemaHandler interface {
	// InterfaceID 获取接口ID
	InterfaceID() string
	// GetHandle http-Get请求（读数据）
	GetHandle(ctx context.Context, params map[string]string) (*APIRsp, error)
	// PostHandle http-Post请求（写数据）
	PostHandle(ctx context.Context, params map[string]string) (*APIRsp, error)
}

// APIHandle API入口
type APIHandle struct {
	// 添加接口处理器映射
	handlers map[string]SchemaHandler
}

// GetAPIHandleInstance 返回API处理器的全局单例实例
func GetAPIHandleInstance() *APIHandle {
	apiHandleOnce.Do(func() {
		apiHandleInstance = NewAPIHandle()
	})
	return apiHandleInstance
}

var NewAPIHandle = func() *APIHandle {
	return &APIHandle{
		handlers: make(map[string]SchemaHandler),
	}
}

// Register 注册接口处理器
func (a *APIHandle) Register(handler SchemaHandler) {
	if a.handlers == nil {
		a.handlers = make(map[string]SchemaHandler)
	}
	a.handlers[handler.InterfaceID()] = handler
	log.Infof("[API Router] 已注册处理器: %s", handler.InterfaceID())
}

// GetRegisteredHandlers 获取所有已注册的处理器ID
func (a *APIHandle) GetRegisteredHandlers() []string {
	handlers := make([]string, 0, len(a.handlers))
	for id := range a.handlers {
		handlers = append(handlers, id)
	}
	return handlers
}

// httpMux 全局HTTP ServeMux用于注册额外的HTTP路由
var httpMux *http.ServeMux

// GetHTTPMux 获取全局HTTP ServeMux
func GetHTTPMux() *http.ServeMux {
	if httpMux == nil {
		httpMux = http.NewServeMux()
	}
	return httpMux
}

// RegisterStandardHTTPHandlers 注册标准http接口
func RegisterStandardHTTPHandlers(s *server.Server) {
	// 打印当前已注册的处理器
	log.Infof("[API Router] 当前已注册的处理器: %v", GetAPIHandleInstance().GetRegisteredHandlers())

	router := mux.NewRouter()

	// 确保httpMux已初始化
	mux := GetHTTPMux()

	// 先注册额外的HTTP路由，让它们优先匹配
	router.PathPrefix("/moox-api/cloud_node/").Handler(mux)

	// 然后注册通用的API路由
	router.HandleFunc("/moox-api/{interfaceid}", func(w http.ResponseWriter, r *http.Request) {
		err := handleAPIRequest(w, r)

		// 处理响应状态
		if err == nil {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(errs.Msg(err)))
		}
	})

	// 最后注册其他HTTP路由作为fallback
	router.PathPrefix("/").Handler(mux)

	thttp.RegisterNoProtocolServiceMux(s.Service("trpc.moox.api.stdhttp"), router)
}

func handleAPIRequest(w http.ResponseWriter, r *http.Request) error {
	ctx := r.Context()
	params := make(map[string]string)

	if r.Method == http.MethodGet {
		// 处理GET请求，提取URL查询参数
		queryParams := r.URL.Query()
		for key, values := range queryParams {
			if len(values) > 0 {
				params[key] = values[0]
			}
		}
	} else if r.Method == http.MethodPost {
		// 处理POST请求，提取JSON内容
		// 先解码到 interface{} 以处理混合类型
		var jsonParams map[string]interface{}
		if err := jsoniter.NewDecoder(r.Body).Decode(&jsonParams); err != nil {
			log.ErrorContextf(ctx, "解析请求体失败: %+v. 跳过", err)
			return nil
		}
		// 转换所有值为字符串
		for key, value := range jsonParams {
			params[key] = fmt.Sprintf("%v", value)
		}
	} else {
		log.ErrorContextf(ctx, "不支持的HTTP方法: %s", r.Method)
		return nil
	}

	// 从URL路径中获取interfaceid
	vars := mux.Vars(r)
	interfaceID, ok := vars["interfaceid"]
	if !ok || interfaceID == "" {
		return errs.New(400, "请求错误:未提供有效的interfaceid")
	}

	// 根据interfaceID获取对应的接口处理器
	handler, ok := GetAPIHandleInstance().handlers[interfaceID]
	if !ok {
		log.ErrorContextf(ctx, "[API Router] 未找到处理器: %s, 当前已注册: %v",
			interfaceID, GetAPIHandleInstance().GetRegisteredHandlers())
		return errs.New(404, "未找到处理器: "+interfaceID)
	}

	// 根据HTTP方法选择调用GetHandle或PostHandle
	switch r.Method {
	case http.MethodGet:
		rsp, err := handler.GetHandle(ctx, params)
		if err != nil {
			return err
		}
		handleResponse(w, rsp)
	case http.MethodPost:
		rsp, err := handler.PostHandle(ctx, params)
		if err != nil {
			return err
		}
		handleResponse(w, rsp)
	default:
		return errs.New(405, "不支持的HTTP方法: "+r.Method)
	}
	return nil
}

// BuildResponse 构建http响应
func BuildResponse(w http.ResponseWriter, jsonData map[string]any) {
	json := simplejson.New()
	for key, val := range jsonData {
		json.Set(key, val)
	}
	data, err := json.MarshalJSON()
	if err != nil {
		log.Errorf("Fail to create jsonStr of response, err is:%s.", err)
		return
	}
	if _, err := w.Write(data); err != nil {
		log.Errorf("Fail to write data to response, err is:%s.", err)
	}
}

// APIRsp 接口统一的返回信息（兼容wuji返回格式）
type APIRsp struct {
	// Code 错误码（200标识成功）
	Code int `json:"code"`
	// Data 数据列表
	Data []any `json:"data"`
}

// handleResponse 处理API响应
func handleResponse(w http.ResponseWriter, rsp *APIRsp) {
	if rsp != nil {
		BuildResponse(w, map[string]any{
			"code": rsp.Code,
			"data": rsp.Data,
		})
	}
}
