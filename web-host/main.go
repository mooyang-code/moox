package main

import (
	"compress/gzip"
	"crypto/md5"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	_ "github.com/mooyang-code/moox/web-host/internal/statik" // 导入生成的静态资源包
	"github.com/rakyll/statik/fs"
)

// gzipResponseWriter 实现gzip压缩的ResponseWriter
type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

// 优化的静态文件处理器，支持缓存和gzip压缩
func optimizedStaticHandler(statikFS http.FileSystem, w http.ResponseWriter, r *http.Request) {
	// 记录请求路径用于调试
	log.Printf("静态文件请求: %s", r.URL.Path)

	// 确定实际请求的文件路径
	actualPath := r.URL.Path

	// 对于根路径，使用 index.html
	if actualPath == "/" {
		actualPath = "/index.html"
	}

	// 打开文件
	file, err := statikFS.Open(actualPath)
	if err != nil {
		// 对于静态资源（JS/CSS等），如果找不到就返回404
		// 只有HTML请求才回退到index.html (SPA路由)
		if isStaticAsset(actualPath) {
			log.Printf("静态资源未找到: %s", actualPath)
			http.NotFound(w, r)
			return
		}

		// SPA路由：所有非静态资源路径都返回index.html
		file, err = statikFS.Open("/index.html")
		if err != nil {
			log.Printf("index.html未找到: %s", err)
			http.NotFound(w, r)
			return
		}
		// 对于SPA路由，使用index.html的路径
		actualPath = "/index.html"
	}
	defer file.Close()

	// 设置正确的Content-Type（基于实际文件路径）
	setContentType(w, actualPath)

	// 获取文件信息
	stat, err := file.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}


	// 生成ETag
	etag := generateETag(stat.Name(), stat.ModTime(), stat.Size())

	// 设置缓存头
	setCacheHeaders(w, actualPath, etag)

	// 检查客户端缓存
	if checkClientCache(r, etag, stat.ModTime()) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// 读取文件内容到内存（因为statik文件系统可能不支持Seek）
	// 对于小文件这是可接受的
	content, err := io.ReadAll(file)
	if err != nil {
		log.Printf("读取文件失败: %s - %v", r.URL.Path, err)
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	// 设置Content-Length
	w.Header().Set("Content-Length", fmt.Sprintf("%d", len(content)))

	// 检查是否支持gzip压缩
	if shouldCompress(r, actualPath) {
		w.Header().Set("Content-Encoding", "gzip")
		// 对于gzip压缩，需要删除Content-Length
		w.Header().Del("Content-Length")

		gz := gzip.NewWriter(w)
		defer gz.Close()

		// 写入压缩内容
		if _, err := gz.Write(content); err != nil {
			log.Printf("压缩写入失败: %s - %v", r.URL.Path, err)
			return
		}
	} else {
		// 直接写入内容
		if _, err := w.Write(content); err != nil {
			log.Printf("写入响应失败: %s - %v", r.URL.Path, err)
			return
		}
	}
}

// 生成ETag
func generateETag(name string, modTime time.Time, size int64) string {
	hash := md5.Sum([]byte(fmt.Sprintf("%s%d%d", name, modTime.Unix(), size)))
	return fmt.Sprintf(`"%x"`, hash)
}

// 设置缓存头
func setCacheHeaders(w http.ResponseWriter, path, etag string) {
	w.Header().Set("ETag", etag)
	
	// 根据文件类型设置不同的缓存策略
	if isStaticAsset(path) {
		// 静态资源（JS、CSS、图片等）缓存1年
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else if strings.HasSuffix(path, ".html") || path == "/" {
		// HTML文件缓存1小时，但允许重新验证
		w.Header().Set("Cache-Control", "public, max-age=3600, must-revalidate")
	} else {
		// 其他文件缓存1天
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
}

// 检查客户端缓存
func checkClientCache(r *http.Request, etag string, modTime time.Time) bool {
	// 检查If-None-Match
	if inm := r.Header.Get("If-None-Match"); inm != "" {
		return inm == etag
	}
	
	// 检查If-Modified-Since
	if ims := r.Header.Get("If-Modified-Since"); ims != "" {
		if t, err := time.Parse(http.TimeFormat, ims); err == nil {
			return modTime.Before(t.Add(1 * time.Second))
		}
	}
	
	return false
}

// 判断是否为静态资源
func isStaticAsset(path string) bool {
	staticExts := []string{".js", ".css", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".woff", ".woff2", ".ttf", ".eot"}
	for _, ext := range staticExts {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

// 判断是否应该压缩
func shouldCompress(r *http.Request, path string) bool {
	// 检查客户端是否支持gzip
	if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
		return false
	}

	// 检查文件类型是否适合压缩
	compressibleTypes := []string{".html", ".css", ".js", ".json", ".xml", ".svg", ".txt"}
	for _, ext := range compressibleTypes {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}

	return false
}

// 设置正确的Content-Type
func setContentType(w http.ResponseWriter, path string) {
	contentTypes := map[string]string{
		".html": "text/html; charset=utf-8",
		".css":  "text/css; charset=utf-8",
		".js":   "application/javascript; charset=utf-8",
		".mjs":  "application/javascript; charset=utf-8",
		".json": "application/json; charset=utf-8",
		".png":  "image/png",
		".jpg":  "image/jpeg",
		".jpeg": "image/jpeg",
		".gif":  "image/gif",
		".svg":  "image/svg+xml",
		".ico":  "image/x-icon",
		".woff": "font/woff",
		".woff2": "font/woff2",
		".ttf":  "font/ttf",
		".eot":  "application/vnd.ms-fontobject",
		".xml":  "application/xml; charset=utf-8",
		".txt":  "text/plain; charset=utf-8",
	}

	// 根据文件扩展名设置Content-Type
	for ext, contentType := range contentTypes {
		if strings.HasSuffix(path, ext) {
			w.Header().Set("Content-Type", contentType)
			return
		}
	}

	// 默认Content-Type
	w.Header().Set("Content-Type", "application/octet-stream")
}

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

		// 否则提供静态文件服务，添加缓存和压缩
		optimizedStaticHandler(statikFS, w, r)
	})

	// 启动服务器
	log.Println("服务器启动在 http://localhost:19527")
	log.Println("静态文件服务: /")
	log.Println("API代理转发: /gateway/* -> http://localhost:20103/gateway/*")
	log.Fatal(http.ListenAndServe(":19527", nil))
}
