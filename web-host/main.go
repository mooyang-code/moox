package main

import (
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	_ "github.com/mooyang-code/moox/web-host/internal/statik" // 导入生成的静态资源包
	"github.com/rakyll/statik/fs"
)

func main() {
	// 创建静态文件系统
	statikFS, err := fs.New()
	if err != nil {
		log.Fatal(err)
	}

	// 配置后端服务地址
	backendURL, err := url.Parse("http://localhost:20103")
	if err != nil {
		log.Fatal(err)
	}

	// 创建反向代理
	proxy := httputil.NewSingleHostReverseProxy(backendURL)

	// 设置路由
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// 如果是 /gateway 开头的请求，转发到后端
		if strings.HasPrefix(r.URL.Path, "/gateway") {
			log.Printf("转发请求到后端: %s", r.URL.Path)
			proxy.ServeHTTP(w, r)
			return
		}

		// 否则提供静态文件服务
		http.FileServer(statikFS).ServeHTTP(w, r)
	})

	// 启动服务器
	log.Println("服务器启动在 http://localhost:9527")
	log.Println("静态文件服务: /")
	log.Println("API代理转发: /gateway/* -> http://localhost:20103/gateway/*")
	log.Fatal(http.ListenAndServe(":9527", nil))
}
