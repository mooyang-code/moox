package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gorilla/mux"
)

// 模拟的存储服务响应
func mockStorageService() {
	router := mux.NewRouter()

	// 模拟 ListProjects 接口
	router.HandleFunc("/trpc.storage.metadata.MetaAdmin/ListProjects", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// 模拟项目数据
		response := map[string]interface{}{
			"code":    0,
			"message": "获取项目列表成功",
			"projects": []map[string]interface{}{
				{
					"proj_id":   1,
					"proj_name": "测试项目1",
					"remark":    "这是第一个测试项目",
					"ctime":     time.Now().Format("2006-01-02 15:04:05"),
					"mtime":     time.Now().Format("2006-01-02 15:04:05"),
					"invalid":   0,
				},
				{
					"proj_id":   2,
					"proj_name": "测试项目2",
					"remark":    "这是第二个测试项目",
					"ctime":     time.Now().AddDate(0, 0, -1).Format("2006-01-02 15:04:05"),
					"mtime":     time.Now().Format("2006-01-02 15:04:05"),
					"invalid":   0,
				},
			},
		}

		log.Printf("模拟存储服务收到请求: %s", r.URL.Path)
		json.NewEncoder(w).Encode(response)
	}).Methods("POST")

	// 模拟 CreateProject 接口
	router.HandleFunc("/trpc.storage.metadata.MetaAdmin/CreateProject", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		response := map[string]interface{}{
			"code":    0,
			"message": "项目创建成功",
			"proj_id": 123,
		}

		log.Printf("模拟存储服务收到创建项目请求: %s", r.URL.Path)
		json.NewEncoder(w).Encode(response)
	}).Methods("POST")

	log.Println("模拟存储服务启动在端口 :8080")
	log.Fatal(http.ListenAndServe(":8080", router))
}

// 模拟的网关服务
func mockGatewayService() {
	router := mux.NewRouter()

	// 设置CORS
	router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Access-Control-Allow-Origin", "*")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-App-Id, X-App-Key, X-Access-Token, X-Trace-Id")

			if r.Method == "OPTIONS" {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	})

	// 控制台 API 转发路由: /api/admin/{service}/{method}
	router.HandleFunc("/api/admin/{service}/{method}", func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)
		service := vars["service"]
		method := vars["method"]

		// 提取头部信息
		appID := r.Header.Get("X-App-Id")
		appKey := r.Header.Get("X-App-Key")
		traceID := r.Header.Get("X-Trace-Id")

		log.Printf("网关收到请求: 服务=%s, 方法=%s, AppID=%s, TraceID=%s", service, method, appID, traceID)

		// 验证必要的头部信息
		if appID == "" || appKey == "" {
			http.Error(w, "缺少必要的认证头部信息 X-App-Id 和 X-App-Key", http.StatusBadRequest)
			return
		}

		// 读取请求体
		var bodyBytes []byte
		if r.Body != nil {
			var err error
			bodyBytes, err = io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, fmt.Sprintf("读取请求体失败: %v", err), http.StatusBadRequest)
				return
			}
		}

		// 模拟转发到底层服务
		if service == "storage" {
			// 构建目标URL
			targetURL := fmt.Sprintf("http://127.0.0.1:8080/trpc.%s.metadata.MetaAdmin/%s", service, method)

			// 准备请求体
			requestBody := map[string]interface{}{
				"auth_info": map[string]interface{}{
					"app_id":  appID,
					"app_key": appKey,
				},
			}

			// 如果有业务参数，合并进去
			if len(bodyBytes) > 0 {
				var businessParams map[string]interface{}
				if err := json.Unmarshal(bodyBytes, &businessParams); err == nil {
					for k, v := range businessParams {
						requestBody[k] = v
					}
				}
			}

			// 序列化请求体
			reqBody, _ := json.Marshal(requestBody)

			// 创建HTTP客户端
			client := &http.Client{Timeout: 5 * time.Second}

			// 发送请求到底层服务
			req, _ := http.NewRequest("POST", targetURL, bytes.NewBuffer(reqBody))
			req.Header.Set("Content-Type", "application/json")
			if traceID != "" {
				req.Header.Set("X-Trace-ID", traceID)
			}

			resp, err := client.Do(req)
			if err != nil {
				http.Error(w, fmt.Sprintf("调用底层服务失败: %v", err), http.StatusInternalServerError)
				return
			}
			defer resp.Body.Close()

			// 转发响应
			w.Header().Set("Content-Type", "application/json")
			if traceID != "" {
				w.Header().Set("X-Trace-Id", traceID)
			}

			io.Copy(w, resp.Body)
		} else {
			http.Error(w, fmt.Sprintf("不支持的服务: %s", service), http.StatusBadRequest)
		}
	}).Methods("GET", "POST", "PUT", "DELETE")

	// 健康检查接口
	router.HandleFunc("/api/admin/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":   "ok",
			"time":     time.Now().Format("2006-01-02 15:04:05"),
			"services": []string{"storage", "auth"},
		})
	}).Methods("GET")

	log.Println("网关服务启动在端口 :18202")
	log.Fatal(http.ListenAndServe(":18202", router))
}

func main() {
	// 启动模拟存储服务
	go mockStorageService()

	// 等待存储服务启动
	time.Sleep(1 * time.Second)

	fmt.Println("==================================")
	fmt.Println("路径转发网关服务示例")
	fmt.Println("==================================")
	fmt.Println("网关服务: http://localhost:18202")
	fmt.Println("存储服务: http://localhost:8080")
	fmt.Println("")
	fmt.Println("使用方式:")
	fmt.Println("1. 获取项目列表:")
	fmt.Printf(`
curl -X POST http://localhost:18202/api/admin/storage/ListProjects \
  -H "Content-Type: application/json" \
  -H "X-App-Id: test123" \
  -H "X-App-Key: test123" \
  -H "X-Trace-Id: trace-12345" \
  -d '{}'
`)
	fmt.Println("")
	fmt.Println("2. 创建项目:")
	fmt.Printf(`
curl -X POST http://localhost:18202/api/admin/storage/CreateProject \
  -H "Content-Type: application/json" \
  -H "X-App-Id: test123" \
  -H "X-App-Key: test123" \
  -H "X-Trace-Id: trace-67890" \
  -d '{
    "proj_name": "新项目",
    "remark": "这是一个新项目"
  }'
`)
	fmt.Println("")
	fmt.Println("3. 健康检查:")
	fmt.Println("curl http://localhost:18202/api/admin/health")
	fmt.Println("")

	// 启动网关服务
	mockGatewayService()
}
