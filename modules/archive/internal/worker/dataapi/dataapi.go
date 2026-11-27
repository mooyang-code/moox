// Package dataapi 是 worker 只听 127.0.0.1 的数据端口：下载分区归档文件（GET /v1/partition，配置了对象存储时读远端副本，
// 未配置对象存储时直接校验下载本地已导出的 .csv.gz）、查看暂存库里某个标的最近的行（GET /v1/recent），
// 以及 moox-cli import 的归档目标写入批次（POST /v1/import/rows、/v1/import/finish）。
// 端口不验签：外部只能经 control 的中转（按服务目录只放行 console 与 moox-cli）、SSH 本地转发或 control 本机的 Admin 进入；
// 浏览器的鉴权由 Admin 的一次性 ticket 完成，Admin 只转发两个 GET 路径。写接口只接受 worker 正在执行的导入运行。
package dataapi

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/mooyang-code/moox/modules/archive/internal/objectstore"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/export"
	"github.com/mooyang-code/moox/modules/archive/internal/worker/staging"
	storagepb "github.com/mooyang-code/moox/modules/storage/proto/storagegen"
	"google.golang.org/protobuf/proto"
	"trpc.group/trpc-go/trpc-go/log"
)

const (
	// MaxDownloads 是同时进行的下载上限，超出返回 429。
	MaxDownloads = 2
	// holdBytes 是下载时扣住不发的末尾字节数：读完核对 sha256 相符才写出，不符就中断连接，客户端拿到的文件长度不足。
	holdBytes = 64 << 10
	// DefaultRecentLimit、MaxRecentLimit 是最新数据的默认行数与上限。
	DefaultRecentLimit = 200
	MaxRecentLimit     = 500
	maxSubjectLength   = 256
	// MaxImportBodyBytes 是一次导入写入的请求体上限（protobuf 编码的 PrimaryUpsertFieldsReq；moox-cli 每批 1000 行）。
	MaxImportBodyBytes = 8 << 20
)

// 导入写接口的错误：ImportSink 返回它们（可包一层说明），处理器据此给出状态码。
var (
	// ErrImportNotFound：运行不存在、不是导入运行或已经结束（404）。
	ErrImportNotFound = errors.New("没有正在执行的这条导入运行")
	// ErrDiskInsufficient：worker 可用磁盘低于下限（507）。
	ErrDiskInsufficient = errors.New("worker 可用磁盘低于下限，拒绝写入")
	// ErrInvalidBatch：批次不合法，整批拒绝（400）。
	ErrInvalidBatch = errors.New("导入批次不合法")
)

// ImportResult 是一批行写进暂存区的结果（响应 JSON）。
type ImportResult struct {
	Added   uint64 `json:"added"`
	Changed uint64 `json:"changed"`
	Same    uint64 `json:"same"`
}

// ImportSink 把导入批次交给正在执行的导入运行；只有运行协程写暂存区，串行由它保证。
type ImportSink interface {
	Write(ctx context.Context, runID string, req *storagepb.PrimaryUpsertFieldsReq) (ImportResult, error)
	Finish(runID string) error
}

// SHA256Header 是下载响应里分区文件 sha256 的响应头。
const SHA256Header = "X-Moox-SHA256"

// Remote 是对象存储的流式读接口。
type Remote interface {
	Open(ctx context.Context, key string) (*objectstore.Stream, error)
}

// Options 是数据端口的依赖。
type Options struct {
	// StatePath 是暂存库文件路径；每次请求用只读连接打开，用完关闭。
	StatePath string
	// Files 用来定位 dataset.json 与本地已导出的归档文件。
	Files export.Files
	// Remote 为 nil 表示没有配置对象存储，GET /v1/partition 直接读取本地已导出的归档文件并校验 SHA-256。
	Remote Remote
	// Imports 接收导入批次；nil 时写接口一律 404。
	Imports ImportSink
	// BytesPerSecond 是全部下载共享的带宽上限。
	BytesPerSecond int64
	// OpenStore 只读打开暂存库，默认 staging.OpenReadOnly（测试替换）。
	OpenStore func(path string) (*staging.Store, error)
	// Sleep 是限速等待，默认按真实时间（测试替换）。
	Sleep func(ctx context.Context, d time.Duration) error
	Now   func() time.Time
}

// Server 是数据端口的 HTTP 处理器。
type Server struct {
	opts      Options
	downloads chan struct{}
	bucket    *bucket
	mux       *http.ServeMux
}

// New 创建处理器。
func New(opts Options) *Server {
	if opts.OpenStore == nil {
		opts.OpenStore = staging.OpenReadOnly
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Sleep == nil {
		opts.Sleep = sleep
	}
	s := &Server{opts: opts, downloads: make(chan struct{}, MaxDownloads), bucket: newBucket(opts.BytesPerSecond, opts.Now, opts.Sleep), mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /v1/partition", s.partition)
	s.mux.HandleFunc("GET /v1/recent", s.recent)
	s.mux.HandleFunc("POST /v1/import/rows", s.importRows)
	s.mux.HandleFunc("POST /v1/import/finish", s.importFinish)
	return s
}

// ServeHTTP 实现 http.Handler。
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) { s.mux.ServeHTTP(w, r) }

// writeError 写出与 Admin 裸处理器一致的 JSON 错误。
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": status, "msg": message, "success": false})
}

// taskParams 读取并校验 space、dataset。
func taskParams(r *http.Request) (space, dataset string, err error) {
	space, dataset = r.URL.Query().Get("space"), r.URL.Query().Get("dataset")
	if export.CheckComponent(space) != nil || export.CheckComponent(dataset) != nil {
		return "", "", errors.New("space、dataset 不能为空，也不能含斜杠或以点开头")
	}
	return space, dataset, nil
}

// lookup 用只读连接取分区状态与任务目录，取完立刻关闭（开始流式下载之前就释放连接）。
func (s *Server) lookup(ctx context.Context, space, dataset, key string) (staging.PartitionState, string, bool, error) {
	store, err := s.opts.OpenStore(s.opts.StatePath)
	if err != nil {
		return staging.PartitionState{}, "", false, err
	}
	defer store.Close()
	p, ok, err := store.Partition(ctx, space, dataset, key)
	if err != nil || !ok {
		return p, "", ok, err
	}
	_, dirName, bound, err := store.TaskDir(ctx, space, dataset)
	if err != nil || !bound {
		return p, "", bound, err
	}
	return p, dirName, true, nil
}

func (s *Server) partition(w http.ResponseWriter, r *http.Request) {
	space, dataset, err := taskParams(r)
	key := r.URL.Query().Get("partition")
	if err == nil && export.CheckComponent(key) != nil {
		err = errors.New("partition 不能为空，也不能含斜杠或以点开头")
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	select {
	case s.downloads <- struct{}{}:
		defer func() { <-s.downloads }()
	default:
		writeError(w, http.StatusTooManyRequests, fmt.Sprintf("同时下载的数量已达上限（%d 个），请稍后再试", MaxDownloads))
		return
	}
	ctx := r.Context()
	p, dirName, ok, err := s.lookup(ctx, space, dataset, key)
	if err != nil {
		log.Errorf("archive data api: 查询分区 %s/%s/%s 失败: %v", space, dataset, key, err)
		writeError(w, http.StatusInternalServerError, "查询分区登记失败")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "该分区不在当前执行器的登记里")
		return
	}
	var (
		reader io.ReadCloser
		size   int64
	)
	if s.opts.Remote == nil {
		if p.FilePath == "" || p.SHA256 == "" {
			writeError(w, http.StatusConflict, "本地归档文件未就绪：该分区还没有导出")
			return
		}
		rel := filepath.FromSlash(p.FilePath)
		if strings.Contains(p.FilePath, "\\") || !filepath.IsLocal(rel) || rel == "." {
			log.Errorf("archive data api: 非法本地归档路径 %q", p.FilePath)
			writeError(w, http.StatusInternalServerError, "读取本地归档文件失败")
			return
		}
		root, err := os.OpenRoot(s.opts.Files.Root)
		if err != nil {
			log.Errorf("archive data api: 打开归档根目录失败: %v", err)
			writeError(w, http.StatusInternalServerError, "读取本地归档文件失败")
			return
		}
		defer root.Close()
		file, err := root.Open(rel)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				writeError(w, http.StatusConflict, "本地归档文件未就绪：磁盘上找不到该分区文件")
				return
			}
			log.Errorf("archive data api: 打开本地归档文件 %s 失败: %v", p.FilePath, err)
			writeError(w, http.StatusInternalServerError, "读取本地归档文件失败")
			return
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() {
			_ = file.Close()
			log.Errorf("archive data api: 检查本地归档文件 %s 失败: %v", p.FilePath, err)
			writeError(w, http.StatusInternalServerError, "读取本地归档文件失败")
			return
		}
		reader, size = file, info.Size()
	} else {
		if !p.Replicated() {
			writeError(w, http.StatusConflict, "远端副本未就绪：当前版本还没有上传或还没有校验")
			return
		}
		stream, err := s.opts.Remote.Open(ctx, p.Remote.Key)
		if err != nil {
			log.Errorf("archive data api: 读取远端副本 %s 失败: %v", p.Remote.Key, err)
			if errors.Is(err, objectstore.ErrNotFound) {
				writeError(w, http.StatusConflict, "远端副本未就绪：对象存储里找不到该文件")
				return
			}
			writeError(w, http.StatusBadGateway, "读取远端副本失败")
			return
		}
		reader, size = stream, stream.Size
	}
	defer reader.Close()
	name := path.Base(p.FilePath)
	ext := strings.TrimPrefix(name, strings.SplitN(name, ".", 2)[0])
	w.Header().Set("Content-Type", objectstore.ContentType(name))
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s_%s%s"`, dirName, p.Key, ext))
	w.Header().Set(SHA256Header, p.SHA256)
	w.WriteHeader(http.StatusOK)
	if err := s.verifiedCopy(ctx, w, reader, p.SHA256); err != nil {
		// 已写出的字节少于 Content-Length，返回后 net/http 会关闭连接，客户端拿到的是长度不足的响应。
		log.Errorf("archive data api: 下载 %s/%s/%s 中断: %v", space, dataset, key, err)
	}
}

// errSHAMismatch 表示分区文件的内容与登记的 sha256 不符。
var errSHAMismatch = errors.New("分区文件的 sha256 与登记不符")

// verifiedCopy 边读边算 sha256，按令牌桶限速写出，并始终扣住最后 holdBytes 字节：读完核对相符才写出最后一段。
func (s *Server) verifiedCopy(ctx context.Context, w io.Writer, r io.Reader, want string) error {
	hash := sha256.New()
	buf := make([]byte, holdBytes+32<<10)
	held := 0
	for {
		n, err := r.Read(buf[held:])
		if n > 0 {
			hash.Write(buf[held : held+n])
			held += n
			if over := held - holdBytes; over > 0 {
				if werr := s.write(ctx, w, buf[:over]); werr != nil {
					return werr
				}
				held = copy(buf, buf[over:held])
			}
		}
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
	}
	if got := hex.EncodeToString(hash.Sum(nil)); got != want {
		return fmt.Errorf("%w（登记 %s，实际 %s）", errSHAMismatch, want, got)
	}
	return s.write(ctx, w, buf[:held])
}

func (s *Server) write(ctx context.Context, w io.Writer, p []byte) error {
	if len(p) == 0 {
		return nil
	}
	if err := s.bucket.take(ctx, int64(len(p))); err != nil {
		return err
	}
	_, err := w.Write(p)
	return err
}

// recentResponse 是 /v1/recent 的响应。单元格是按 CSV 规则格式化的字符串，缺失值为 null。
type recentResponse struct {
	Columns []staging.RecentColumn `json:"columns"`
	Rows    [][]*string            `json:"rows"`
}

func (s *Server) recent(w http.ResponseWriter, r *http.Request) {
	space, dataset, err := taskParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	query := r.URL.Query()
	subject := query.Get("subject")
	if subject == "" || len(subject) > maxSubjectLength || !utf8.ValidString(subject) || strings.ContainsAny(subject, "\x00\r\n") {
		writeError(w, http.StatusBadRequest, "subject 不能为空，最长 256 字节，不能含控制字符")
		return
	}
	var before time.Time
	if raw := query.Get("before"); raw != "" {
		if before, err = time.Parse(time.RFC3339Nano, raw); err != nil {
			writeError(w, http.StatusBadRequest, "before 必须是 RFC3339 时间")
			return
		}
		if err := staging.CheckNanosRange(before); err != nil {
			writeError(w, http.StatusBadRequest, "before 超出可查询范围（1678～2261 年）")
			return
		}
	}
	limit := DefaultRecentLimit
	if raw := query.Get("limit"); raw != "" {
		if limit, err = strconv.Atoi(raw); err != nil || limit < 1 || limit > MaxRecentLimit {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("limit 必须在 1 到 %d 之间", MaxRecentLimit))
			return
		}
	}
	ctx := r.Context()
	store, err := s.opts.OpenStore(s.opts.StatePath)
	if err != nil {
		log.Errorf("archive data api: 只读打开暂存库失败: %v", err)
		writeError(w, http.StatusInternalServerError, "打开暂存库失败")
		return
	}
	defer store.Close()
	purpose, dirName, ok, err := store.TaskDir(ctx, space, dataset)
	if err != nil {
		log.Errorf("archive data api: 查询任务 %s/%s 失败: %v", space, dataset, err)
		writeError(w, http.StatusInternalServerError, "查询任务失败")
		return
	}
	if !ok {
		writeError(w, http.StatusNotFound, "该任务不在当前执行器上")
		return
	}
	columns, err := s.datasetColumns(space, purpose, dirName)
	if errors.Is(err, os.ErrNotExist) {
		writeError(w, http.StatusConflict, "数据集说明（dataset.json）还没有生成，等任务首次运行后再查看")
		return
	}
	if err != nil {
		log.Errorf("archive data api: 读取 %s/%s 的 dataset.json 失败: %v", space, dataset, err)
		writeError(w, http.StatusInternalServerError, "读取数据集说明失败")
		return
	}
	task := staging.Task{SpaceID: space, DatasetID: dataset, Purpose: purpose, DirName: dirName, Columns: columns}
	rows, err := store.Recent(ctx, task, subject, before, limit)
	if err != nil {
		log.Errorf("archive data api: 查询 %s/%s 最新数据失败: %v", space, dataset, err)
		writeError(w, http.StatusInternalServerError, "查询最新数据失败")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(recentResponse{Columns: rows.Columns, Rows: rows.Rows})
}

// importRows 处理 POST /v1/import/rows?run=<run_id>：请求体是 protobuf 编码的 PrimaryUpsertFieldsReq，
// 交给导入运行写进暂存区，写完回 {"added","changed","same"}。
func (s *Server) importRows(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Query().Get("run")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run 不能为空")
		return
	}
	if s.opts.Imports == nil {
		writeError(w, http.StatusNotFound, ErrImportNotFound.Error())
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxImportBodyBytes))
	if err != nil {
		writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("读取请求体失败（上限 %d 字节）: %v", MaxImportBodyBytes, err))
		return
	}
	req := &storagepb.PrimaryUpsertFieldsReq{}
	if err := proto.Unmarshal(body, req); err != nil {
		writeError(w, http.StatusBadRequest, "请求体不是 PrimaryUpsertFieldsReq: "+err.Error())
		return
	}
	result, err := s.opts.Imports.Write(r.Context(), runID, req)
	if err != nil {
		writeError(w, importStatus(err), err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(result)
}

// importFinish 处理 POST /v1/import/finish?run=<run_id>：通知导入运行批次已全部写完，立即返回 202。
func (s *Server) importFinish(w http.ResponseWriter, r *http.Request) {
	runID := r.URL.Query().Get("run")
	if runID == "" {
		writeError(w, http.StatusBadRequest, "run 不能为空")
		return
	}
	if s.opts.Imports == nil {
		writeError(w, http.StatusNotFound, ErrImportNotFound.Error())
		return
	}
	if err := s.opts.Imports.Finish(runID); err != nil {
		writeError(w, importStatus(err), err.Error())
		return
	}
	w.WriteHeader(http.StatusAccepted)
}

func importStatus(err error) int {
	switch {
	case errors.Is(err, ErrImportNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrDiskInsufficient):
		return http.StatusInsufficientStorage
	case errors.Is(err, ErrInvalidBatch):
		return http.StatusBadRequest
	default:
		log.Errorf("archive data api: 导入写入失败: %v", err)
		return http.StatusInternalServerError
	}
}

// datasetColumns 读 dataset.json 里的值列（去掉三个系统列）。
func (s *Server) datasetColumns(space, purpose, dirName string) ([]staging.Column, error) {
	rel, err := export.DatasetJSONPath(space, purpose, dirName)
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(s.opts.Files.Abs(rel))
	if err != nil {
		return nil, err
	}
	var meta export.DatasetMeta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return nil, fmt.Errorf("解析 dataset.json: %w", err)
	}
	var columns []staging.Column
	for _, c := range meta.Columns {
		switch c.Name {
		case "data_time", "subject_id", "series_tag":
			continue
		}
		columns = append(columns, staging.Column{Name: c.Name, Type: c.Type})
	}
	return columns, nil
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
