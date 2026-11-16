package webserver

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
)

// minGzipBytes 以下的文件不压缩。
const minGzipBytes = 512

// staticFile 是启动时载入内存的一个前端文件，ETag 按内容算（embed 里的文件没有修改时间）。
type staticFile struct {
	body, gzipped []byte
	etag          string
	contentType   string
	cacheControl  string
}

// staticSite 提供内嵌的前端：SPA 路由回退到 index.html，按内容的 ETag，可压缩的文件预先压好 gzip。
type staticSite struct {
	files map[string]*staticFile
}

func newStaticSite(root fs.FS) (*staticSite, error) {
	site := &staticSite{files: map[string]*staticFile{}}
	err := fs.WalkDir(root, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		// 占位文件（.gitkeep）之类的隐藏文件不对外提供。
		if strings.HasPrefix(path.Base(name), ".") {
			return nil
		}
		body, err := fs.ReadFile(root, name)
		if err != nil {
			return err
		}
		urlPath := "/" + name
		sum := sha256.Sum256(body)
		file := &staticFile{body: body, etag: `"` + hex.EncodeToString(sum[:16]) + `"`, contentType: contentType(urlPath), cacheControl: cacheControl(urlPath)}
		if compressible(urlPath) && len(body) >= minGzipBytes {
			var buf bytes.Buffer
			writer, _ := gzip.NewWriterLevel(&buf, gzip.BestCompression)
			_, _ = writer.Write(body)
			if err := writer.Close(); err != nil {
				return err
			}
			if buf.Len() < len(body) {
				file.gzipped = buf.Bytes()
			}
		}
		site.files[urlPath] = file
		return nil
	})
	return site, err
}

func (s *staticSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	name := path.Clean("/" + r.URL.Path)
	if name == "/" {
		name = "/index.html"
	}
	file := s.files[name]
	if file == nil {
		// 静态资源找不到就是 404；其余路径是前端路由，回退到 index.html。
		if isAsset(name) {
			http.NotFound(w, r)
			return
		}
		if file = s.files["/index.html"]; file == nil {
			http.Error(w, "管理台前端没有打包进这个程序：用 scripts/build/build.sh admin 构建。", http.StatusServiceUnavailable)
			return
		}
	}
	header := w.Header()
	header.Set("Content-Type", file.contentType)
	header.Set("Cache-Control", file.cacheControl)
	if strings.HasPrefix(file.contentType, "text/html") {
		header.Set("Pragma", "no-cache")
		header.Set("Expires", "0")
	}
	body, etag, encoding := file.body, file.etag, ""
	if file.gzipped != nil {
		header.Add("Vary", "Accept-Encoding")
		if acceptsGzip(r.Header.Get("Accept-Encoding")) {
			// 压缩后是另一种表示，ETag 也要不同。
			body, etag, encoding = file.gzipped, strings.TrimSuffix(file.etag, `"`)+`-gzip"`, "gzip"
		}
	}
	header.Set("ETag", etag)
	if etagMatches(r.Header.Get("If-None-Match"), etag) {
		header.Del("Content-Type")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	if encoding != "" {
		header.Set("Content-Encoding", encoding)
	}
	header.Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(body)
	}
}

// etagMatches 处理 If-None-Match 里的逗号分隔列表与 *，弱校验前缀 W/ 也认。
func etagMatches(header, etag string) bool {
	for _, item := range strings.Split(header, ",") {
		item = strings.TrimPrefix(strings.TrimSpace(item), "W/")
		if item == "*" || item == etag {
			return true
		}
	}
	return false
}

// acceptsGzip 判断 Accept-Encoding 是否接受 gzip（q=0 表示不接受）。
func acceptsGzip(header string) bool {
	for _, item := range strings.Split(header, ",") {
		coding, params, _ := strings.Cut(item, ";")
		if !strings.EqualFold(strings.TrimSpace(coding), "gzip") {
			continue
		}
		if q, ok := strings.CutPrefix(strings.TrimSpace(params), "q="); ok {
			value, err := strconv.ParseFloat(strings.TrimSpace(q), 64)
			return err == nil && value > 0
		}
		return true
	}
	return false
}

var assetExtensions = []string{".js", ".mjs", ".css", ".map", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".webp", ".woff", ".woff2", ".ttf", ".eot"}

func isAsset(name string) bool {
	for _, ext := range assetExtensions {
		if strings.HasSuffix(name, ext) {
			return true
		}
	}
	return false
}

// cacheControl：入口 index.html 不缓存，否则发布后旧页面会引用已不存在的带哈希文件；
// static/ 下是带内容哈希的构建产物，缓存一年；其余（public 目录原样拷入的文件）缓存一天。
func cacheControl(name string) string {
	switch {
	case strings.HasSuffix(name, ".html"):
		return "no-store, no-cache, must-revalidate, max-age=0"
	case strings.HasPrefix(name, "/static/") && isAsset(name):
		return "public, max-age=31536000, immutable"
	default:
		return "public, max-age=86400"
	}
}

var contentTypes = map[string]string{
	".html":  "text/html; charset=utf-8",
	".css":   "text/css; charset=utf-8",
	".js":    "application/javascript; charset=utf-8",
	".mjs":   "application/javascript; charset=utf-8",
	".json":  "application/json; charset=utf-8",
	".map":   "application/json; charset=utf-8",
	".png":   "image/png",
	".jpg":   "image/jpeg",
	".jpeg":  "image/jpeg",
	".gif":   "image/gif",
	".svg":   "image/svg+xml",
	".ico":   "image/x-icon",
	".webp":  "image/webp",
	".woff":  "font/woff",
	".woff2": "font/woff2",
	".ttf":   "font/ttf",
	".eot":   "application/vnd.ms-fontobject",
	".xml":   "application/xml; charset=utf-8",
	".txt":   "text/plain; charset=utf-8",
}

func contentType(name string) string {
	if value, ok := contentTypes[path.Ext(name)]; ok {
		return value
	}
	return "application/octet-stream"
}

func compressible(name string) bool {
	switch path.Ext(name) {
	case ".html", ".css", ".js", ".mjs", ".json", ".map", ".svg", ".xml", ".txt", ".ttf", ".eot", ".ico":
		return true
	}
	return false
}
