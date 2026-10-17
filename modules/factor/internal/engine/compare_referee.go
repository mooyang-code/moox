package engine

import (
	"math"
	"math/big"

	"github.com/mooyang-code/moox/packages/factorexpr"
	"github.com/mooyang-code/moox/packages/factorexpr/exec"
)

// referee 用 256 位浮点沿表达式树重新计算一个点，作为新旧结果差异的裁判（只用于 compare，不追求速度）。
// 只支持生产因子用到的算子；遇到不支持的算子、NaN、除以 0 时返回 ok=false。
type referee struct {
	prec    uint // 256 是高精度参考；53 模拟逐步舍入到 float64 的朴素实现，用来估计 float64 固有的误差
	panel   *exec.Panel
	columns map[string]int
	series  int
	memo    map[refereeKey]refereeValue
}

type refereeKey struct {
	node *factorexpr.Node
	t    int
}

type refereeValue struct {
	value *big.Float
	ok    bool
}

const refereePrecision = 256

func newReferee(panel *exec.Panel, columns []string, series int, prec uint) *referee {
	index := make(map[string]int, len(columns))
	for i, column := range columns {
		index[column] = i
	}
	return &referee{prec: prec, panel: panel, columns: index, series: series, memo: map[refereeKey]refereeValue{}}
}

func (r *referee) bigOf(v float64) *big.Float { return new(big.Float).SetPrec(r.prec).SetFloat64(v) }
func (r *referee) bigNew() *big.Float         { return new(big.Float).SetPrec(r.prec) }

// eval 返回节点在网格下标 t 的高精度值；布尔值为 1 或 0。
func (r *referee) eval(node *factorexpr.Node, t int) (*big.Float, bool) {
	key := refereeKey{node, t}
	if cached, ok := r.memo[key]; ok {
		return cached.value, cached.ok
	}
	value, ok := r.compute(node, t)
	r.memo[key] = refereeValue{value, ok}
	return value, ok
}

func (r *referee) compute(node *factorexpr.Node, t int) (*big.Float, bool) {
	if t < 0 || t >= r.panel.Length() {
		return nil, false
	}
	switch node.Op {
	case factorexpr.OpConst:
		return r.bigOf(node.Value), true
	case factorexpr.OpColumn:
		column, ok := r.columns[node.Column]
		if !ok {
			return nil, false
		}
		v := r.panel.Value(column, r.series, t)
		if math.IsNaN(v) {
			return nil, false
		}
		return r.bigOf(v), true
	}
	args := func() ([]*big.Float, bool) {
		values := make([]*big.Float, len(node.Args))
		for i, arg := range node.Args {
			v, ok := r.eval(arg, t)
			if !ok {
				return nil, false
			}
			values[i] = v
		}
		return values, true
	}
	window := func(arg *factorexpr.Node, n int) ([]*big.Float, bool) {
		values := make([]*big.Float, n)
		for i := 0; i < n; i++ {
			v, ok := r.eval(arg, t-n+1+i)
			if !ok {
				return nil, false
			}
			values[i] = v
		}
		return values, true
	}
	boolean := func(b bool) (*big.Float, bool) {
		if b {
			return r.bigOf(1), true
		}
		return r.bigOf(0), true
	}
	switch node.Op {
	case "add", "sub", "mul", "div", "gt", "ge", "lt", "le", "eq", "ne", "and", "or":
		v, ok := args()
		if !ok {
			return nil, false
		}
		a, b := v[0], v[1]
		switch node.Op {
		case "add":
			return r.bigNew().Add(a, b), true
		case "sub":
			return r.bigNew().Sub(a, b), true
		case "mul":
			return r.bigNew().Mul(a, b), true
		case "div":
			if b.Sign() == 0 {
				return nil, false
			}
			return r.bigNew().Quo(a, b), true
		case "gt":
			return boolean(a.Cmp(b) > 0)
		case "ge":
			return boolean(a.Cmp(b) >= 0)
		case "lt":
			return boolean(a.Cmp(b) < 0)
		case "le":
			return boolean(a.Cmp(b) <= 0)
		case "eq":
			return boolean(a.Cmp(b) == 0)
		case "ne":
			return boolean(a.Cmp(b) != 0)
		case "and":
			return boolean(a.Sign() != 0 && b.Sign() != 0)
		case "or":
			return boolean(a.Sign() != 0 || b.Sign() != 0)
		}
	case "neg", "abs", "sqrt", "not":
		v, ok := args()
		if !ok {
			return nil, false
		}
		switch node.Op {
		case "neg":
			return r.bigNew().Neg(v[0]), true
		case "abs":
			return r.bigNew().Abs(v[0]), true
		case "sqrt":
			if v[0].Sign() < 0 {
				return nil, false
			}
			return r.bigNew().Sqrt(v[0]), true
		case "not":
			return boolean(v[0].Sign() == 0)
		}
	case "where":
		cond, ok := r.eval(node.Args[0], t)
		if !ok {
			return nil, false
		}
		if cond.Sign() != 0 {
			return r.eval(node.Args[1], t)
		}
		return r.eval(node.Args[2], t)
	case "delay", "delta", "pct_change":
		k := node.Ints[0]
		current, ok := r.eval(node.Args[0], t)
		if !ok {
			return nil, false
		}
		previous, ok := r.eval(node.Args[0], t-k)
		if !ok {
			return nil, false
		}
		switch node.Op {
		case "delay":
			return previous, true
		case "delta":
			return r.bigNew().Sub(current, previous), true
		default:
			if previous.Sign() == 0 {
				return nil, false
			}
			ratio := r.bigNew().Quo(current, previous)
			return ratio.Sub(ratio, r.bigOf(1)), true
		}
	case "ts_sum", "ts_mean", "ts_min", "ts_max", "ts_std", "ts_var", "ts_rank":
		n := node.Ints[0]
		w, ok := window(node.Args[0], n)
		if !ok {
			return nil, false
		}
		switch node.Op {
		case "ts_sum", "ts_mean":
			sum := r.bigNew()
			for _, v := range w {
				sum.Add(sum, v)
			}
			if node.Op == "ts_mean" {
				sum.Quo(sum, r.bigOf(float64(n)))
			}
			return sum, true
		case "ts_min", "ts_max":
			best := w[0]
			for _, v := range w[1:] {
				if (node.Op == "ts_min" && v.Cmp(best) < 0) || (node.Op == "ts_max" && v.Cmp(best) > 0) {
					best = v
				}
			}
			return best, true
		case "ts_std", "ts_var":
			mean := r.bigNew()
			for _, v := range w {
				mean.Add(mean, v)
			}
			mean.Quo(mean, r.bigOf(float64(n)))
			ss := r.bigNew()
			for _, v := range w {
				d := r.bigNew().Sub(v, mean)
				ss.Add(ss, d.Mul(d, d))
			}
			ss.Quo(ss, r.bigOf(float64(n-1)))
			if node.Op == "ts_std" {
				return ss.Sqrt(ss), true
			}
			return ss, true
		case "ts_rank":
			current := w[n-1]
			less, equal := 0, 0
			for _, v := range w {
				switch v.Cmp(current) {
				case -1:
					less++
				case 0:
					equal++
				}
			}
			return r.bigOf((float64(less) + float64(equal+1)/2) / float64(n)), true
		}
	}
	return nil, false
}

// refereeValueAt 返回输出根节点在 (序列, t) 的高精度值（转成 float64）。
func refereeValueAt(panel *exec.Panel, columns []string, series int, root *factorexpr.Node, t int) (float64, bool) {
	return refereeAt(panel, columns, series, root, t, refereePrecision)
}

func refereeAt(panel *exec.Panel, columns []string, series int, root *factorexpr.Node, t int, prec uint) (float64, bool) {
	value, ok := newReferee(panel, columns, series, prec).eval(root, t)
	if !ok {
		return 0, false
	}
	f, _ := value.Float64()
	return f, true
}

// floatErrorMargin 是“在 float64 固有误差之内”的放宽倍数：新实现的误差不超过朴素 float64 实现误差的这么多倍即视为
// 受 float64 精度所限，而不是实现错误。
const floatErrorMargin = 10

// withinFloatError 报告连续值差异是否在 float64 固有误差之内：朴素实现（逐步舍入到 53 位）相对高精度值的误差为 E，
// 新实现的误差不超过 max(10E, 1e-12·|参考|)。
func withinFloatError(panel *exec.Panel, columns []string, series int, root *factorexpr.Node, t int, current float64) bool {
	reference, ok := refereeAt(panel, columns, series, root, t, refereePrecision)
	if !ok {
		return false
	}
	naive, ok := refereeAt(panel, columns, series, root, t, 53)
	if !ok {
		return false
	}
	bound := math.Max(floatErrorMargin*math.Abs(naive-reference), 1e-12*math.Abs(reference))
	return math.Abs(current-reference) <= bound
}

// nearTieWithinFloatError 报告 ts_rank 的跳变是否发生在 float64 分辨不出的近似并列上：窗口内有某个值与当期值的高精度差，
// 不超过窗口内朴素 float64 实现最大误差的 10 倍。
func nearTieWithinFloatError(panel *exec.Panel, columns []string, series int, rank *factorexpr.Node, t int) bool {
	if rank.Op != "ts_rank" {
		return false
	}
	arg, n := rank.Args[0], rank.Ints[0]
	precise := newReferee(panel, columns, series, refereePrecision)
	naive := newReferee(panel, columns, series, 53)
	values := make([]*big.Float, n)
	maxError := 0.0
	for i := 0; i < n; i++ {
		p, ok := precise.eval(arg, t-n+1+i)
		if !ok {
			return false
		}
		q, ok := naive.eval(arg, t-n+1+i)
		if !ok {
			return false
		}
		pf, _ := p.Float64()
		qf, _ := q.Float64()
		maxError = math.Max(maxError, math.Abs(pf-qf))
		values[i] = p
	}
	if maxError == 0 {
		return false
	}
	current := values[n-1]
	for _, v := range values[:n-1] {
		d, _ := new(big.Float).SetPrec(refereePrecision).Sub(v, current).Float64()
		if math.Abs(d) <= floatErrorMargin*maxError {
			return true
		}
	}
	return false
}
