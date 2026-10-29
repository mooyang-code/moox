// zstddict 训练、评估和审查 wirecompress 使用的 zstd 字典。不进任何服务二进制。
//
//	zstddict train -corpus DIR -out FILE [-size 224] [-id 0x4D580001] [-cap 3] [-normalize]
//	zstddict eval  -corpus DIR -dict FILE [-normalize=false]
//	zstddict scan  -dict FILE
//
// 语料目录按来源分子目录（写、读、元数据、控制面、事件行、指标快照、事件其他、计划等），每个样本一个文件。
// 训练调用 zstd 命令行的 --train（COVER 系列）：klauspost 的 zstd.BuildDict 需要调用方自己提供字典主体，不做片段选择。
package main

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/klauspost/compress/zstd"
)

const dictMagic = 0xEC30A437

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "train":
		err = train(os.Args[2:])
	case "eval":
		err = eval(os.Args[2:])
	case "scan":
		err = scan(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "用法：zstddict train|eval|scan -h")
	os.Exit(2)
}

// samples 返回语料目录里每个来源的样本文件，来源按名字排序。
func samples(corpus string) (map[string][]string, []string, error) {
	entries, err := os.ReadDir(corpus)
	if err != nil {
		return nil, nil, err
	}
	out := map[string][]string{}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		files, _ := filepath.Glob(filepath.Join(corpus, entry.Name(), "*.bin"))
		sort.Strings(files)
		if len(files) > 0 {
			out[entry.Name()] = files
			names = append(names, entry.Name())
		}
	}
	if len(names) == 0 {
		return nil, nil, fmt.Errorf("语料目录 %s 下没有任何 <来源>/*.bin 样本", corpus)
	}
	sort.Strings(names)
	return out, names, nil
}

// 训练样本里只出现一次、不会再命中的内容：占着字典空间却没有用。
var normalizers = []struct {
	pattern *regexp.Regexp
	replace string
}{
	{regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`), "00000000-0000-0000-0000-000000000000"},
	{regexp.MustCompile(`[0-9a-f]{40,}`), strings.Repeat("0", 64)},
	{regexp.MustCompile(`20\d\d-\d\d-\d\dT\d\d:\d\d:\d\d(\.\d+)?Z`), "2026-01-01T00:00:00.000000000Z"},
	// 标的列表 {"id":"X","i":N},… 只对当前市场有效：同一段里只保留前几项。
	{regexp.MustCompile(`(\{"id":"[^"]+","i":\d+\},){8}(\{"id":"[^"]+","i":\d+\},?)+`), `{"id":"SYMBOL","i":0},{"id":"SYMBOL","i":1},{"id":"SYMBOL","i":2},{"id":"SYMBOL","i":3},{"id":"SYMBOL","i":4},{"id":"SYMBOL","i":5},{"id":"SYMBOL","i":6},{"id":"SYMBOL","i":7}`},
}

func normalize(data []byte) []byte {
	for _, n := range normalizers {
		data = n.pattern.ReplaceAll(data, []byte(n.replace))
	}
	return data
}

func train(args []string) error {
	flags := flag.NewFlagSet("train", flag.ExitOnError)
	corpus := flags.String("corpus", "", "语料目录")
	out := flags.String("out", "", "输出字典文件")
	sizeKB := flags.Int("size", 224, "字典大小（KB）")
	id := flags.String("id", "", "字典 ID，例如 0x4D580001（必填，写进 zstd 帧头）")
	capMB := flags.Int("cap", 3, "每个来源抽样的字节上限（MB）")
	doNormalize := flags.Bool("normalize", false, "训练前归一化一次性内容（摘要、UUID、时间戳、标的列表）；是否值得请先用 eval 对比")
	_ = flags.Parse(args)
	if *corpus == "" || *out == "" || *id == "" {
		return fmt.Errorf("-corpus、-out、-id 必填")
	}
	dictID, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(*id), "0x"), 16, 32)
	if err != nil || dictID < 32768 {
		return fmt.Errorf("字典 ID 必须是 ≥0x8000 的十六进制数（zstd 的用户区间）")
	}
	if _, err := exec.LookPath("zstd"); err != nil {
		return fmt.Errorf("训练需要 zstd 命令行（%w）", err)
	}
	groups, names, err := samples(*corpus)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp("", "zstddict-train-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	capBytes := *capMB << 20
	total := 0
	for _, name := range names {
		files := groups[name]
		var sum int
		sizes := make([]int, len(files))
		for i, f := range files {
			info, err := os.Stat(f)
			if err != nil {
				return err
			}
			sizes[i] = int(info.Size())
			sum += sizes[i]
		}
		// 等间隔抽样，让样本覆盖整个时间范围，而不是只取开头。
		stride := float64(sum) / float64(capBytes)
		if stride < 1 {
			stride = 1
		}
		picked, acc, next := 0, 0, 0.0
		for i, f := range files {
			acc += sizes[i]
			if float64(acc) < next {
				continue
			}
			data, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			if len(data) == 0 {
				continue
			}
			if *doNormalize {
				data = normalize(data)
			}
			if err := os.WriteFile(filepath.Join(work, fmt.Sprintf("%s_%06d.bin", name, i)), data, 0o600); err != nil {
				return err
			}
			picked += len(data)
			next += stride
			if picked >= capBytes {
				break
			}
		}
		fmt.Printf("来源 %-14s 样本 %5d 个，抽样 %8d 字节\n", name, len(files), picked)
		total += picked
	}
	cmd := exec.Command("zstd", "-q", "--train", "-r", work, fmt.Sprintf("--maxdict=%d", *sizeKB*1024), "-o", *out)
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("zstd --train：%w", err)
	}
	data, err := os.ReadFile(*out)
	if err != nil {
		return err
	}
	if len(data) < 8 || binary.LittleEndian.Uint32(data[:4]) != dictMagic {
		return fmt.Errorf("zstd 产出的不是 zstd 字典")
	}
	binary.LittleEndian.PutUint32(data[4:8], uint32(dictID))
	if err := os.WriteFile(*out, data, 0o644); err != nil {
		return err
	}
	fmt.Printf("训练语料 %d 字节 → 字典 %s（%d 字节，ID %#x）\n", total, *out, len(data), dictID)
	return nil
}

func eval(args []string) error {
	flags := flag.NewFlagSet("eval", flag.ExitOnError)
	corpus := flags.String("corpus", "", "评估语料目录（与训练语料分开，按时间留出）")
	dictPath := flags.String("dict", "", "字典文件")
	_ = flags.Parse(args)
	if *corpus == "" || *dictPath == "" {
		return fmt.Errorf("-corpus、-dict 必填")
	}
	dict, err := os.ReadFile(*dictPath)
	if err != nil {
		return err
	}
	plain, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderCRC(false), zstd.WithEncoderConcurrency(1))
	if err != nil {
		return err
	}
	withDict, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedDefault), zstd.WithEncoderCRC(false), zstd.WithEncoderConcurrency(1), zstd.WithEncoderDict(dict))
	if err != nil {
		return err
	}
	decoder, err := zstd.NewReader(nil, zstd.WithDecoderConcurrency(1), zstd.WithDecoderDicts(dict))
	if err != nil {
		return err
	}
	groups, names, err := samples(*corpus)
	if err != nil {
		return err
	}
	fmt.Printf("%-14s %6s %12s %8s %8s %8s\n", "来源", "样本", "原始字节", "gzip", "无字典", "字典")
	var rawAll, gzAll, plainAll, dictAll int
	for _, name := range names {
		var raw, gz, pl, dc int
		for _, f := range groups[name] {
			data, err := os.ReadFile(f)
			if err != nil {
				return err
			}
			var buf bytes.Buffer
			w := gzip.NewWriter(&buf)
			_, _ = w.Write(data)
			_ = w.Close()
			packed := withDict.EncodeAll(data, nil)
			back, err := decoder.DecodeAll(packed, nil)
			if err != nil || !bytes.Equal(back, data) {
				return fmt.Errorf("%s 往返不一致：%v", f, err)
			}
			raw += len(data)
			gz += buf.Len()
			pl += len(plain.EncodeAll(data, nil))
			dc += len(packed)
		}
		fmt.Printf("%-14s %6d %12d %7.2fx %7.2fx %7.2fx\n", name, len(groups[name]), raw, ratio(raw, gz), ratio(raw, pl), ratio(raw, dc))
		rawAll, gzAll, plainAll, dictAll = rawAll+raw, gzAll+gz, plainAll+pl, dictAll+dc
	}
	fmt.Printf("%-14s %6s %12d %7.2fx %7.2fx %7.2fx\n", "合计", "", rawAll, ratio(rawAll, gzAll), ratio(rawAll, plainAll), ratio(rawAll, dictAll))
	return nil
}

func ratio(raw, compressed int) float64 {
	if compressed == 0 {
		return 0
	}
	return float64(raw) / float64(compressed)
}

var sensitive = []struct {
	name    string
	pattern *regexp.Regexp
}{
	{"敏感关键字", regexp.MustCompile(`(?i)(secret|passw|token|webhook|api[_-]?key|private[_-]?key|credential|hmac|authoriz|bearer|signature)`)},
	{"URL", regexp.MustCompile(`https?://`)},
	{"邮箱", regexp.MustCompile(`[\w.+-]+@[\w-]+\.[\w.]+`)},
	{"IPv4", regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)},
	{"长十六进制", regexp.MustCompile(`\b[0-9a-fA-F]{32,}\b`)},
	{"UUID", regexp.MustCompile(`\b[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\b`)},
}

// scan 提取字典里 8 个字符以上的可打印串，按类别统计疑似敏感项，供人工确认后再提交字典。
func scan(args []string) error {
	flags := flag.NewFlagSet("scan", flag.ExitOnError)
	dictPath := flags.String("dict", "", "字典文件")
	_ = flags.Parse(args)
	if *dictPath == "" {
		return fmt.Errorf("-dict 必填")
	}
	data, err := os.ReadFile(*dictPath)
	if err != nil {
		return err
	}
	printable := regexp.MustCompile(`[ -~]{8,}`).FindAll(data, -1)
	fmt.Printf("可打印串 %d 条\n", len(printable))
	for _, item := range sensitive {
		var hits []string
		for _, s := range printable {
			if item.pattern.Match(s) {
				hits = append(hits, string(s))
			}
		}
		fmt.Printf("%-10s %d 条\n", item.name, len(hits))
		for i, hit := range hits {
			if i == 5 {
				break
			}
			if len(hit) > 100 {
				hit = hit[:100] + "…"
			}
			fmt.Printf("    %s\n", hit)
		}
	}
	return nil
}
