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
	// 打开文件
	file, err := statikFS.Open(r.URL.Path)
	if err != nil {
		// 如果文件不存在，尝试index.html (SPA路由)
		if r.URL.Path != "/" {
			file, err = statikFS.Open("/index.html")
			if err != nil {
				http.NotFound(w, r)
				return
			}
		} else {
			http.NotFound(w, r)
			return
		}
	}
	defer file.Close()

	// 获取文件信息
	stat, err := file.Stat()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// 生成ETag
	etag := generateETag(stat.Name(), stat.ModTime(), stat.Size())
	
	// 设置缓存头
	setCacheHeaders(w, r.URL.Path, etag)
	
	// 检查客户端缓存
	if checkClientCache(r, etag, stat.ModTime()) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	// 检查是否支持gzip压缩
	if shouldCompress(r, r.URL.Path) {
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		
		gzipWriter := gzipResponseWriter{Writer: gz, ResponseWriter: w}
		http.ServeContent(gzipWriter, r, stat.Name(), stat.ModTime(), file)
	} else {
		http.ServeContent(w, r, stat.Name(), stat.ModTime(), file)
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
	log.Println("服务器启动在 http://localhost:9527")
	log.Println("静态文件服务: /")
	log.Println("API代理转发: /gateway/* -> http://localhost:20103/gateway/*")
	log.Fatal(http.ListenAndServe(":9527", nil))
}
