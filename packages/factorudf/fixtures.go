package factorudf

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
)

// Num 是 fixtures 里的数值：NaN 写成 null。
type Num float64

func (n *Num) UnmarshalJSON(b []byte) error {
	if bytes.Equal(bytes.TrimSpace(b), []byte("null")) {
		*n = Num(math.NaN())
		return nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err != nil {
		return err
	}
	*n = Num(f)
	return nil
}

func (n Num) MarshalJSON() ([]byte, error) {
	f := float64(n)
	if math.IsNaN(f) {
		return []byte("null"), nil
	}
	if math.IsInf(f, 0) {
		return nil, fmt.Errorf("fixtures 里不允许 ±Inf")
	}
	return []byte(strconv.FormatFloat(f, 'g', -1, 64)), nil
}

// FixtureCase 是一条序列的对拍样本：inputs 的长度 = expected 的长度 + 窗口长度 − 1。
// RelTol 为 0 要求逐位一致，否则允许相对误差。
type FixtureCase struct {
	Name     string  `json:"name"`
	Params   []Num   `json:"params"`
	Inputs   [][]Num `json:"inputs"`
	Expected []Num   `json:"expected"`
	RelTol   float64 `json:"rel_tol,omitempty"`
}

// FixtureFile 是一个 fixtures 文件。
type FixtureFile struct {
	Cases []FixtureCase `json:"cases"`
}

// 单个 fixtures 的总量限制：随包上传的 fixtures 不超过 256 KiB。
const MaxFixturesBytes = 256 << 10

// NormalizeFixtures 解析并重新序列化（紧凑、字段顺序固定），随包上传的就是这份规范化的内容；
// 服务端与本地跑的是完全相同的一套测试。
func NormalizeFixtures(raw []byte) ([]byte, error) {
	var f FixtureFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("fixtures 不是合法的 JSON：%w", err)
	}
	if len(f.Cases) == 0 {
		return nil, fmt.Errorf("fixtures 没有任何样本")
	}
	for i, c := range f.Cases {
		if c.Name == "" || len(c.Inputs) == 0 || len(c.Expected) == 0 {
			return nil, fmt.Errorf("第 %d 个样本缺少 name、inputs 或 expected", i)
		}
		if c.RelTol < 0 || math.IsNaN(c.RelTol) || math.IsInf(c.RelTol, 0) {
			return nil, fmt.Errorf("样本 %q 的 rel_tol 不合法", c.Name)
		}
		for _, in := range c.Inputs {
			if len(in) != len(c.Inputs[0]) {
				return nil, fmt.Errorf("样本 %q 的各输入长度不一致", c.Name)
			}
		}
	}
	return json.Marshal(f)
}

// FixtureSet 是一组命名的 fixtures 文件（平铺的文件名 → 规范化内容）。
type FixtureSet map[string][]byte

func (s FixtureSet) names() []string {
	names := make([]string, 0, len(s))
	for n := range s {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// TotalBytes 返回全部 fixtures 的字节数。
func (s FixtureSet) TotalBytes() int {
	n := 0
	for _, b := range s {
		n += len(b)
	}
	return n
}
