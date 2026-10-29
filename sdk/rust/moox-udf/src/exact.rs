//! 精确求和：结果是真实和四舍五入到最近的 f64（半数取偶），与求和顺序无关。
//!
//! 只在确实需要逐位一致的累加语义时使用（例如与内置算子对拍）；默认写朴素循环，精确求和比朴素累加慢得多。
//! 部分和用不重叠表示（Shewchuk，与 Python 的 `math.fsum` 相同）。项的量级跨度很大时部分和会变长，成本随之增加。

/// 增量的精确累加器。
#[derive(Default)]
pub struct ExactSum {
    partials: Vec<f64>,
}

impl ExactSum {
    pub fn new() -> Self {
        ExactSum {
            partials: Vec::new(),
        }
    }

    /// 加入一项。输入要是有限值（引擎里 ±Inf 已记为 NaN；含 NaN 时结果无意义，调用方应先排除）。
    pub fn add(&mut self, value: f64) {
        let mut x = value;
        let mut i = 0;
        for j in 0..self.partials.len() {
            let mut y = self.partials[j];
            if x.abs() < y.abs() {
                std::mem::swap(&mut x, &mut y);
            }
            let hi = x + y;
            let lo = y - (hi - x);
            if lo != 0.0 {
                self.partials[i] = lo;
                i += 1;
            }
            x = hi;
        }
        self.partials.truncate(i);
        self.partials.push(x);
    }

    /// 正确舍入的结果（CPython `math_fsum` 的末段，含半数取偶的修正）。
    pub fn round(&self) -> f64 {
        let p = &self.partials;
        let mut n = p.len();
        if n == 0 {
            return 0.0;
        }
        n -= 1;
        let mut hi = p[n];
        let mut lo = 0.0;
        while n > 0 {
            let x = hi;
            n -= 1;
            let y = p[n];
            hi = x + y;
            lo = y - (hi - x);
            if lo != 0.0 {
                break;
            }
        }
        if n > 0 && ((lo < 0.0 && p[n - 1] < 0.0) || (lo > 0.0 && p[n - 1] > 0.0)) {
            let y = lo * 2.0;
            let x = hi + y;
            if y == x - hi {
                hi = x;
            }
        }
        hi
    }
}

/// 对切片精确求和。
pub fn exact_sum(values: &[f64]) -> f64 {
    let mut acc = ExactSum::new();
    for &v in values {
        acc.add(v);
    }
    acc.round()
}
