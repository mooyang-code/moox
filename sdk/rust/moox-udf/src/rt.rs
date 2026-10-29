//! 宏生成代码使用的运行时：暂存区、ABI 入口与逐点循环（空值规则在这里强制）。用户代码不直接使用它。
#![doc(hidden)]

/// ABI 版本；宿主只接受它。
pub const ABI_VERSION: i32 = 1;

/// 返回码：0 成功；1 参数越界；2 窗口长度不一致；3 指针越界或区域重叠。
pub const OK: i32 = 0;
pub const ERR_PARAM: i32 = 1;
pub const ERR_WINDOW: i32 = 2;
pub const ERR_LAYOUT: i32 = 3;

/// 一个 UDF 的静态描述。
#[derive(Clone, Copy)]
pub struct Spec {
    pub n_inputs: usize,
    pub n_params: usize,
    pub relaxed: bool,
    pub min_valid: usize,
}

/// 最多的输入个数（与宿主元数据校验一致）。
pub const MAX_INPUTS: usize = 8;

/// 逐点循环：对每条序列、每个输出位置应用空值规则后调用用户函数。
///
/// - `inputs[k]` 是第 k 个输入，布局 `[序列][in_len]`；`params` 布局 `[序列][参数]`；`out` 布局 `[序列][out_len]`；
/// - 严格模式：窗口内任一输入有 NaN 输出 NaN，不调用用户函数；
/// - relaxed：每个输入的 NaN 只能出现在窗口开头，否则输出 NaN；各输入从同一时间偏移裁出共同有效段，
///   有效段短于 `min_valid` 或为空时输出 NaN；
/// - 输出只能是有限值或 NaN（±Inf 记为 NaN）。
#[allow(clippy::too_many_arguments)]
pub fn eval_slices<P>(
    spec: &Spec,
    series: usize,
    in_len: usize,
    out_len: usize,
    params: &[f64],
    inputs: &[&[f64]],
    out: &mut [f64],
    parse: fn(&[f64]) -> Option<P>,
    lookback: fn(&P) -> i64,
    call: fn(&[&[f64]], &P) -> f64,
) -> i32 {
    if spec.n_inputs == 0
        || spec.n_inputs > MAX_INPUTS
        || inputs.len() != spec.n_inputs
        || out_len == 0
        || in_len < out_len
        || params.len() != series.saturating_mul(spec.n_params)
        || out.len() != series.saturating_mul(out_len)
        || inputs
            .iter()
            .any(|x| x.len() != series.saturating_mul(in_len))
    {
        return ERR_LAYOUT;
    }
    let mut scratch = Scratch::new(spec, in_len);
    for s in 0..series {
        let p = match parse(&params[s * spec.n_params..(s + 1) * spec.n_params]) {
            Some(p) => p,
            None => return ERR_PARAM,
        };
        let l = lookback(&p);
        if l < 1 || (in_len - out_len + 1) as i64 != l {
            return ERR_WINDOW;
        }
        let l = l as usize;
        let mut series_in: [&[f64]; MAX_INPUTS] = [&[]; MAX_INPUTS];
        for k in 0..spec.n_inputs {
            series_in[k] = &inputs[k][s * in_len..(s + 1) * in_len];
        }
        let series_in = &series_in[..spec.n_inputs];
        scratch.prepare(series_in, spec.relaxed);
        let dst = &mut out[s * out_len..(s + 1) * out_len];
        let mut wins: [&[f64]; MAX_INPUTS] = [&[]; MAX_INPUTS];
        for (j, slot) in dst.iter_mut().enumerate() {
            let b = j + l - 1; // 窗口 [j, b]
            let start = match scratch.valid_start(j, b, spec) {
                Some(start) => start,
                None => {
                    *slot = f64::NAN;
                    continue;
                }
            };
            for k in 0..spec.n_inputs {
                wins[k] = &series_in[k][start..=b];
            }
            let v = call(&wins[..spec.n_inputs], &p);
            *slot = if v.is_finite() { v } else { f64::NAN };
        }
    }
    OK
}

/// 空值规则需要的逐输入索引：每个位置之前（含）最近一个 NaN 的下标与该 NaN 连续段的起点。一条序列算一次。
struct Scratch {
    /// 严格模式：任一输入在位置 i 及之前最近的 NaN 下标（-1 表示没有）。
    any_last: Vec<i64>,
    /// relaxed：每个输入各自的 last_nan 与 run_start。
    last_nan: Vec<Vec<i64>>,
    run_start: Vec<Vec<i64>>,
}

impl Scratch {
    fn new(spec: &Spec, in_len: usize) -> Self {
        if spec.relaxed {
            Scratch {
                any_last: Vec::new(),
                last_nan: (0..spec.n_inputs).map(|_| vec![-1; in_len]).collect(),
                run_start: (0..spec.n_inputs).map(|_| vec![-1; in_len]).collect(),
            }
        } else {
            Scratch {
                any_last: vec![-1; in_len],
                last_nan: Vec::new(),
                run_start: Vec::new(),
            }
        }
    }

    fn prepare(&mut self, series: &[&[f64]], relaxed: bool) {
        if relaxed {
            for (k, x) in series.iter().enumerate() {
                let mut last = -1i64;
                let mut run = -1i64;
                for (i, v) in x.iter().enumerate() {
                    if v.is_nan() {
                        if last != i as i64 - 1 || run < 0 {
                            run = i as i64;
                        }
                        last = i as i64;
                        self.run_start[k][i] = run;
                    } else {
                        self.run_start[k][i] = -1;
                    }
                    self.last_nan[k][i] = last;
                }
            }
        } else {
            let mut last = -1i64;
            for i in 0..self.any_last.len() {
                if series.iter().any(|x| x[i].is_nan()) {
                    last = i as i64;
                }
                self.any_last[i] = last;
            }
        }
    }

    /// 窗口 [a, b] 的有效起点；None 表示这个点输出 NaN。
    fn valid_start(&self, a: usize, b: usize, spec: &Spec) -> Option<usize> {
        if !spec.relaxed {
            return if self.any_last[b] < a as i64 {
                Some(a)
            } else {
                None
            };
        }
        let mut start = a;
        for k in 0..spec.n_inputs {
            let l = self.last_nan[k][b];
            if l >= a as i64 {
                // 窗口里有 NaN：它们必须是一段覆盖窗口开头的连续 NaN。
                if self.run_start[k][l as usize] > a as i64 {
                    return None;
                }
                start = start.max(l as usize + 1);
            }
        }
        if start > b || b + 1 - start < spec.min_valid {
            return None;
        }
        Some(start)
    }
}

// ---------------------------------------------------------------- wasm ABI

/// 暂存区：宿主在其中放参数、输入与输出。8 字节对齐，跨调用复用，下一次 reserve 后旧内容作废。
struct ScratchBuf(std::cell::UnsafeCell<Vec<u64>>);

// wasm 实例是单线程的，不会并发访问。
unsafe impl Sync for ScratchBuf {}

static SCRATCH: ScratchBuf = ScratchBuf(std::cell::UnsafeCell::new(Vec::new()));

/// `moox_udf_reserve`：返回暂存区起始偏移（至少 bytes 字节、8 字节对齐）；长度溢出或内存不足返回 0。
pub fn reserve(bytes: u32) -> u32 {
    let words = (bytes as usize).div_ceil(8).max(1);
    // SAFETY：单线程，且没有借出的引用跨越这次调用（eval 结束就不再持有）。
    let buf = unsafe { &mut *SCRATCH.0.get() };
    if buf.len() < words {
        let extra = words - buf.len();
        if buf.try_reserve_exact(extra).is_err() {
            return 0;
        }
        buf.resize(words, 0);
    }
    buf.as_ptr() as usize as u32
}

/// `moox_udf_eval` 的实现：把三个偏移还原成切片（要求都在暂存区内且互不重叠），然后走 [`eval_slices`]。
#[allow(clippy::too_many_arguments)]
pub fn eval_ptr<P>(
    spec: &Spec,
    series: i32,
    in_len: i32,
    out_len: i32,
    params_ptr: i32,
    inputs_ptr: i32,
    out_ptr: i32,
    parse: fn(&[f64]) -> Option<P>,
    lookback: fn(&P) -> i64,
    call: fn(&[&[f64]], &P) -> f64,
) -> i32 {
    if series < 1 || in_len < 1 || out_len < 1 {
        return ERR_LAYOUT;
    }
    let (series, in_len, out_len) = (series as usize, in_len as usize, out_len as usize);
    let n_params = match series.checked_mul(spec.n_params) {
        Some(n) => n,
        None => return ERR_LAYOUT,
    };
    let in_each = match series.checked_mul(in_len) {
        Some(n) => n,
        None => return ERR_LAYOUT,
    };
    let n_out = match series.checked_mul(out_len) {
        Some(n) => n,
        None => return ERR_LAYOUT,
    };
    let n_in_total = match in_each.checked_mul(spec.n_inputs) {
        Some(n) => n,
        None => return ERR_LAYOUT,
    };
    // SAFETY：单线程；下面的切片只在本次调用内使用，范围经过检查都落在暂存区里且互不重叠。
    let buf = unsafe { &mut *SCRATCH.0.get() };
    let base = buf.as_mut_ptr() as usize;
    let cap = buf.len() * 8;
    let region = |ptr: i32, elems: usize| -> Option<(usize, usize)> {
        let start = ptr as u32 as usize;
        let len = elems.checked_mul(8)?;
        let end = start.checked_add(len)?;
        if start < base || end > base + cap || !start.is_multiple_of(8) {
            return None;
        }
        Some((start, end))
    };
    let (p, i, o) = match (
        region(params_ptr, n_params),
        region(inputs_ptr, n_in_total),
        region(out_ptr, n_out),
    ) {
        (Some(p), Some(i), Some(o)) => (p, i, o),
        _ => return ERR_LAYOUT,
    };
    let disjoint =
        |a: (usize, usize), b: (usize, usize)| a.1 <= b.0 || b.1 <= a.0 || a.0 == a.1 || b.0 == b.1;
    if !(disjoint(p, i) && disjoint(p, o) && disjoint(i, o)) {
        return ERR_LAYOUT;
    }
    let params = unsafe { std::slice::from_raw_parts(p.0 as *const f64, n_params) };
    let flat = unsafe { std::slice::from_raw_parts(i.0 as *const f64, n_in_total) };
    let out = unsafe { std::slice::from_raw_parts_mut(o.0 as *mut f64, n_out) };
    let mut inputs: [&[f64]; MAX_INPUTS] = [&[]; MAX_INPUTS];
    if spec.n_inputs > MAX_INPUTS {
        return ERR_LAYOUT;
    }
    for k in 0..spec.n_inputs {
        inputs[k] = &flat[k * in_each..(k + 1) * in_each];
    }
    eval_slices(
        spec,
        series,
        in_len,
        out_len,
        params,
        &inputs[..spec.n_inputs],
        out,
        parse,
        lookback,
        call,
    )
}
