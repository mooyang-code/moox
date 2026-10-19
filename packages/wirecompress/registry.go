package wirecompress

import (
	"bytes"
	"embed"
	"encoding/binary"
	"fmt"
	"io/fs"
	"runtime"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
	"gopkg.in/yaml.v3"
)

//go:embed dicts/registry.yaml dicts/*.zdict
var embedded embed.FS

const (
	// MinCompressType 与 MaxCompressType 是本包使用的 tRPC 压缩类型号范围；框架内置类型占 0～7。
	MinCompressType = 32
	MaxCompressType = 255

	dictMagic = 0xEC30A437
)

type registryFile struct {
	Dictionaries []dictEntry `yaml:"dictionaries"`
	Bindings     struct {
		Default string            `yaml:"default"`
		Spaces  map[string]string `yaml:"spaces"`
	} `yaml:"bindings"`
}

type dictEntry struct {
	Name         string `yaml:"name"`
	File         string `yaml:"file"`
	ID           uint32 `yaml:"id"`
	CompressType int    `yaml:"compress_type"`
}

// version 是一个不可变的字典版本：字典内容、帧头里的字典 ID 和 tRPC 压缩类型号一经发布不再改变。
type version struct {
	name         string
	id           uint32
	compressType int
	dict         []byte

	once sync.Once
	enc  *zstd.Encoder
	err  error
}

// encoder 第一次用到时才创建，没有用到的字典不占编码器内存。
func (v *version) encoder() (*zstd.Encoder, error) {
	v.once.Do(func() {
		v.enc, v.err = zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.SpeedDefault),
			// TCP/TLS 已保证传输完整性，省掉每帧 4 字节的校验和。
			zstd.WithEncoderCRC(false),
			zstd.WithEncoderConcurrency(concurrency()),
			zstd.WithLowerEncoderMem(true),
			zstd.WithWindowSize(maxWindow),
			// 小于 256 字节的帧默认不写原始长度，而解码端要求每个帧都声明原始长度：强制单段帧，保证总是写入。
			zstd.WithSingleSegment(true),
			zstd.WithEncoderDict(v.dict),
		)
	})
	return v.enc, v.err
}

type registry struct {
	versions []*version
	byType   map[int]*version
	byID     map[uint32]*version
	fallback *version
	spaces   map[string]*version
}

// forSpace 返回空间当前绑定的字典版本；空间为空或没有专用字典时用默认字典。
func (r *registry) forSpace(space string) *version {
	if space = normalizeSpace(space); space != "" {
		if v, ok := r.spaces[space]; ok {
			return v
		}
	}
	return r.fallback
}

func (r *registry) dicts() [][]byte {
	out := make([][]byte, 0, len(r.versions))
	for _, v := range r.versions {
		out = append(out, v.dict)
	}
	return out
}

func normalizeSpace(space string) string { return strings.ToLower(strings.TrimSpace(space)) }

func concurrency() int { return min(runtime.GOMAXPROCS(0), 4) }

// parseRegistry 解析并校验注册表。违反任何规则都返回错误，调用方（进程启动）据此失败。
func parseRegistry(files fs.FS, raw []byte) (*registry, error) {
	var file registryFile
	decoder := yaml.NewDecoder(bytes.NewReader(raw))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return nil, fmt.Errorf("解析字典注册表：%w", err)
	}
	if len(file.Dictionaries) == 0 {
		return nil, fmt.Errorf("字典注册表没有任何字典")
	}
	r := &registry{byType: map[int]*version{}, byID: map[uint32]*version{}, spaces: map[string]*version{}}
	byName := map[string]*version{}
	seenFile := map[string]bool{}
	for _, entry := range file.Dictionaries {
		if entry.Name == "" || entry.File == "" {
			return nil, fmt.Errorf("字典 %q 缺少名字或文件", entry.Name)
		}
		if entry.CompressType < MinCompressType || entry.CompressType > MaxCompressType {
			return nil, fmt.Errorf("字典 %s 的压缩类型号 %d 超出 %d～%d", entry.Name, entry.CompressType, MinCompressType, MaxCompressType)
		}
		if byName[entry.Name] != nil || seenFile[entry.File] || r.byType[entry.CompressType] != nil || r.byID[entry.ID] != nil {
			return nil, fmt.Errorf("字典 %s 的名字、文件、压缩类型号或字典 ID 与其他字典重复", entry.Name)
		}
		dict, err := fs.ReadFile(files, "dicts/"+entry.File)
		if err != nil {
			return nil, fmt.Errorf("读取字典 %s：%w", entry.Name, err)
		}
		if err := checkDict(dict, entry); err != nil {
			return nil, err
		}
		v := &version{name: entry.Name, id: entry.ID, compressType: entry.CompressType, dict: dict}
		byName[entry.Name] = v
		seenFile[entry.File] = true
		r.byType[v.compressType] = v
		r.byID[v.id] = v
		r.versions = append(r.versions, v)
	}
	r.fallback = byName[file.Bindings.Default]
	if r.fallback == nil {
		return nil, fmt.Errorf("默认绑定 %q 不是已有的字典版本", file.Bindings.Default)
	}
	for space, name := range file.Bindings.Spaces {
		if space != normalizeSpace(space) || space == "" {
			return nil, fmt.Errorf("空间 %q 必须是小写且不带空白", space)
		}
		v := byName[name]
		if v == nil {
			return nil, fmt.Errorf("空间 %s 绑定的 %q 不是已有的字典版本", space, name)
		}
		r.spaces[space] = v
	}
	return r, nil
}

// checkDict 校验字典是有效的 zstd 字典，且帧头里的字典 ID 等于注册表里的 id。
func checkDict(dict []byte, entry dictEntry) error {
	if len(dict) < 8 || binary.LittleEndian.Uint32(dict[:4]) != dictMagic {
		return fmt.Errorf("字典 %s 不是 zstd 字典", entry.Name)
	}
	info, err := zstd.InspectDictionary(dict)
	if err != nil {
		return fmt.Errorf("字典 %s 无法解析：%w", entry.Name, err)
	}
	if info.ID() != entry.ID {
		return fmt.Errorf("字典 %s 的文件字典 ID %#x 与注册表的 %#x 不一致", entry.Name, info.ID(), entry.ID)
	}
	return nil
}

var global = mustLoadEmbedded()

func mustLoadEmbedded() *registry {
	raw, err := embedded.ReadFile("dicts/registry.yaml")
	if err != nil {
		panic(fmt.Sprintf("wirecompress: %v", err))
	}
	r, err := parseRegistry(embedded, raw)
	if err != nil {
		panic(fmt.Sprintf("wirecompress: %v", err))
	}
	return r
}
