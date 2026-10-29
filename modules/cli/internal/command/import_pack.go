package command

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/mooyang-code/moox/packages/seriesimport/runner"
	"github.com/mooyang-code/moox/packages/storagepolicy"
)

// 初始数据导入包：
//
//	import-pack/
//	  manifest.json
//	  <freq>/<SUBJECT>.csv
//
// manifest.json 声明目标数据源与每个文件的行数、首尾时间和 sha256。预检按这份声明逐项核对，不合格的包在写入任何内容之前就被拒绝。

const (
	// importPackMinAge 是文件最后一根距现在至少多久：币安的官方日文件通常隔天才发布，合约 REST 在收盘后几秒内还是未定稿值，
	// 留 2 天保证历史段只含已定稿的数据，不会和实时写入撞在一起。
	importPackMinAge = 48 * time.Hour
	// importPackMaxGap 是最后一根距现在最多多久：这段缺口要靠 REST 补采，量大了耗时且受限频影响。
	importPackMaxGap = 30 * 24 * time.Hour
	// importTailRetentionMargin 是尾部窗口起点比保留期截止点多留的余量，免得刚写入的最老一批立刻被清理。
	importTailRetentionMargin = 24 * time.Hour
)

// importPack 是 manifest.json 的内容。
type importPack struct {
	SpaceID   string           `json:"space_id"`
	DatasetID string           `json:"dataset_id"`
	Freq      string           `json:"freq"`
	SeriesTag string           `json:"series_tag"`
	Files     []importPackFile `json:"files"`

	dir string
}

type importPackFile struct {
	SubjectID string `json:"subject_id"`
	File      string `json:"file"`
	Rows      int    `json:"rows"`
	FirstUTC  string `json:"first_utc"`
	LastUTC   string `json:"last_utc"`
	SHA256    string `json:"sha256"`
}

// loadImportPack 读取并检查 manifest.json 本身（结构、文件是否存在、sha256）；与文件内容的比对在 checkImportPack。
func loadImportPack(dir string) (*importPack, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("读取导入包清单：%w", err)
	}
	var pack importPack
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&pack); err != nil {
		return nil, fmt.Errorf("导入包清单 manifest.json 格式不对：%w", err)
	}
	pack.dir = dir
	if pack.SpaceID == "" || pack.DatasetID == "" || pack.Freq == "" {
		return nil, errors.New("导入包清单缺少 space_id、dataset_id 或 freq")
	}
	if len(pack.Files) == 0 {
		return nil, errors.New("导入包清单里没有文件")
	}
	return &pack, nil
}

// paths 返回清单里全部文件的绝对路径（相对清单所在目录）。
func (p *importPack) paths() []string {
	out := make([]string, 0, len(p.Files))
	for _, file := range p.Files {
		out = append(out, filepath.Join(p.dir, file.File))
	}
	return out
}

// checkImportPackFiles 核对每个文件存在、标的与文件名一致、sha256 一致；返回全部问题而不是第一个。
func checkImportPackFiles(pack *importPack) []string {
	var problems []string
	seen := map[string]bool{}
	for _, file := range pack.Files {
		path := filepath.Join(pack.dir, file.File)
		subject := strings.TrimSuffix(filepath.Base(file.File), filepath.Ext(file.File))
		switch {
		case file.SubjectID == "" || file.File == "":
			problems = append(problems, fmt.Sprintf("清单里有条目缺少 subject_id 或 file（%q）", file.File))
			continue
		case file.SubjectID != subject:
			problems = append(problems, fmt.Sprintf("%s：清单的 subject_id %q 与文件名 %q 不一致", file.File, file.SubjectID, subject))
		case seen[file.SubjectID]:
			problems = append(problems, fmt.Sprintf("%s：标的 %s 在清单里重复", file.File, file.SubjectID))
		}
		seen[file.SubjectID] = true
		sum, err := fileSHA256(path)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s：%v", file.File, err))
			continue
		}
		if !strings.EqualFold(sum, file.SHA256) {
			problems = append(problems, fmt.Sprintf("%s：sha256 与清单不一致（文件 %s，清单 %s）", file.File, sum[:12], shortHash(file.SHA256)))
		}
	}
	return problems
}

func shortHash(hash string) string {
	if len(hash) > 12 {
		return hash[:12]
	}
	return hash
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// importPackLimits 是预检用到的外部条件。
type importPackLimits struct {
	Now time.Time
	// Retention 是目标数据集（空间 × 频率）在 Storage 的保留期；RetentionKnown 为假（例如在没有 moox.toml 的因子机上）时跳过与保留期有关的检查。
	Retention      storagepolicy.Period
	RetentionKnown bool
	// MinRows 大于 0 时要求每个文件至少这么多行（该数据源的回看数量 N，保证因子窗口凑得满）。
	MinRows int
}

// maxGap 是文件最后一根距现在的上限：30 天，但不超过保留期减 1 天——补采段必须整段留在 Storage 里，
// 因子机同步器才能从本地最后一根接着拉取，否则这段会被写成填充行。
func (l importPackLimits) maxGap() time.Duration {
	limit := importPackMaxGap
	if l.RetentionKnown && !l.Retention.Forever && l.Retention.Duration > 0 {
		if byRetention := l.Retention.Duration - 24*time.Hour; byRetention < limit {
			limit = byRetention
		}
	}
	return limit
}

// tailCutoff 是只写尾部的目标的行时间下限：早于保留期的行写进去也会在下一次清理时被删。
func (l importPackLimits) tailCutoff() time.Time {
	if !l.RetentionKnown || l.Retention.Forever || l.Retention.Duration <= 0 {
		return time.Time{}
	}
	return l.Now.UTC().Add(-l.Retention.Duration + importTailRetentionMargin)
}

// checkImportPack 把校验扫描的结果（summary）与清单和硬约束比对，返回全部问题。
func checkImportPack(pack *importPack, summary runner.Summary, limits importPackLimits) []string {
	var problems []string
	byFile := map[string]runner.FileSummary{}
	for _, file := range summary.Files {
		byFile[filepath.Clean(file.File)] = file
	}
	maxGap := limits.maxGap()
	for _, entry := range pack.Files {
		got, ok := byFile[filepath.Clean(filepath.Join(pack.dir, entry.File))]
		if !ok {
			problems = append(problems, fmt.Sprintf("%s：没有出现在校验结果里", entry.File))
			continue
		}
		if got.Rows != entry.Rows {
			problems = append(problems, fmt.Sprintf("%s：行数 %d 与清单 %d 不一致", entry.File, got.Rows, entry.Rows))
		}
		for _, edge := range []struct {
			name, declared, actual string
		}{{"first_utc", entry.FirstUTC, got.First}, {"last_utc", entry.LastUTC, got.Last}} {
			declared, err := time.Parse(time.RFC3339, edge.declared)
			actual, actualErr := time.Parse(time.RFC3339, edge.actual)
			if err != nil || actualErr != nil || !declared.Equal(actual) {
				problems = append(problems, fmt.Sprintf("%s：%s 清单写 %q，文件实际是 %q", entry.File, edge.name, edge.declared, edge.actual))
			}
		}
		if limits.MinRows > 0 && got.Rows < limits.MinRows {
			problems = append(problems, fmt.Sprintf("%s：只有 %d 行，少于该数据源的回看数量 %d，因子窗口凑不满", entry.File, got.Rows, limits.MinRows))
		}
		last, err := time.Parse(time.RFC3339, got.Last)
		if err != nil {
			continue
		}
		age := limits.Now.Sub(last)
		switch {
		case age < importPackMinAge:
			problems = append(problems, fmt.Sprintf("%s：最后一根 %s 距现在不足 %s（历史段只能含已定稿的数据，且不能和实时写入重叠）", entry.File, got.Last, importPackMinAge))
		case age > maxGap:
			problems = append(problems, fmt.Sprintf("%s：最后一根 %s 距现在 %s，超过缺口上限 %s（这段要靠 REST 补采，且必须整段留在 Storage 保留期内）", entry.File, got.Last, age.Round(time.Hour), maxGap))
		}
	}
	return problems
}
