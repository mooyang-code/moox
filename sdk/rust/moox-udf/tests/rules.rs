//! 空值规则、窗口视图与有限性的原生测试。

use moox_udf::prelude::*;
use std::cell::Cell;

thread_local! {
    static CALLS: Cell<usize> = const { Cell::new(0) };
}

fn calls() -> usize {
    CALLS.with(|c| c.get())
}

mod strict {
    use super::*;

    #[udf(name = "strict_sum", inputs = [x], params = { n: int(1..=10) }, lookback = "n")]
    fn strict_sum(x: &Window, p: &StrictSumParams) -> f64 {
        let _ = p;
        CALLS.with(|c| c.set(c.get() + 1));
        x.iter().sum()
    }

    pub fn run(input: &[f64], n: f64) -> (i32, Vec<f64>) {
        let out_len = input.len() - n as usize + 1;
        let mut out = vec![0.0; out_len];
        let rc = __moox_udf_native(1, input.len(), out_len, &[n], &[input], &mut out);
        (rc, out)
    }
}

mod relaxed {
    use super::*;

    #[udf(name = "relaxed_pair", inputs = [x, y], params = { n: int(2..=10) }, lookback = "n", nan = "relaxed", min_valid = 2)]
    fn relaxed_pair(x: &Window, y: &Window, p: &RelaxedPairParams) -> f64 {
        let _ = p;
        CALLS.with(|c| c.set(c.get() + 1));
        assert_eq!(x.len(), y.len(), "两个输入的共同有效段等长");
        x.len() as f64
    }

    pub fn run(x: &[f64], y: &[f64], n: f64) -> (i32, Vec<f64>) {
        let out_len = x.len() - n as usize + 1;
        let mut out = vec![0.0; out_len];
        let rc = __moox_udf_native(1, x.len(), out_len, &[n], &[x, y], &mut out);
        (rc, out)
    }
}

const NAN: f64 = f64::NAN;

#[test]
fn strict_returns_nan_for_any_nan_in_window_without_calling() {
    let before = calls();
    let (rc, out) = strict::run(&[1.0, 2.0, NAN, 4.0, 5.0, 6.0], 3.0);
    assert_eq!(rc, 0);
    assert!(out[..3].iter().all(|v| v.is_nan()), "窗口含 NaN");
    assert_eq!(out[3], 15.0);
    assert_eq!(calls() - before, 1, "含 NaN 的窗口不调用用户函数");
}

#[test]
fn strict_all_nan_series() {
    let (rc, out) = strict::run(&[NAN; 6], 2.0);
    assert_eq!(rc, 0);
    assert!(out.iter().all(|v| v.is_nan()));
}

#[test]
fn relaxed_computes_on_common_valid_segment() {
    // x 前 2 个 NaN，y 前 1 个 NaN：窗口 n=4 的共同有效段从两者中较晚的有效起点开始。
    let x = [NAN, NAN, 1.0, 2.0, 3.0, 4.0];
    let y = [NAN, 1.0, 2.0, 3.0, 4.0, 5.0];
    let (rc, out) = relaxed::run(&x, &y, 4.0);
    assert_eq!(rc, 0);
    // 窗口 0..=3：x 有效段 [2,3] 长 2，y 有效段 [1,3]，共同段 [2,3]，长 2 ≥ min_valid。
    assert_eq!(out[0], 2.0);
    // 窗口 1..=4：共同段 [2,4]，长 3。
    assert_eq!(out[1], 3.0);
    assert_eq!(out[2], 4.0);
}

#[test]
fn relaxed_returns_nan_below_min_valid_or_for_middle_nan() {
    let before = calls();
    // 共同有效段只有 1 个点（< min_valid=2）。
    let (_, out) = relaxed::run(&[NAN, NAN, NAN, 1.0], &[NAN, NAN, NAN, 1.0], 4.0);
    assert!(out[0].is_nan());
    // 窗口中间的 NaN：不是开头的连续 NaN，输出 NaN。
    let (_, out) = relaxed::run(&[1.0, NAN, 3.0, 4.0], &[1.0, 2.0, 3.0, 4.0], 4.0);
    assert!(out[0].is_nan());
    // 全空。
    let (_, out) = relaxed::run(&[NAN; 4], &[NAN; 4], 4.0);
    assert!(out[0].is_nan());
    assert_eq!(calls(), before, "这些窗口都不调用用户函数");
}

#[test]
fn relaxed_leading_nan_run_must_cover_window_start() {
    // NaN 段 [1,1] 不覆盖窗口 0..=3 的开头（x[0] 有效）：窗口中间有 NaN → NaN；
    // 窗口 1..=4 的开头就是这段 NaN：共同有效段 [2,4]。
    let (_, out) = relaxed::run(&[1.0, NAN, 3.0, 4.0, 5.0], &[1.0, 2.0, 3.0, 4.0, 5.0], 4.0);
    assert!(out[0].is_nan());
    assert_eq!(out[1], 3.0);
}

#[test]
fn layout_and_param_errors_have_distinct_codes() {
    let mut out = vec![0.0; 1];
    // 参数越界。
    assert_eq!(
        strict::__moox_udf_native(1, 3, 1, &[99.0], &[&[1.0, 2.0, 3.0]], &mut out),
        1
    );
    // 窗口长度与参数不一致（in_len − out_len + 1 ≠ L）。
    assert_eq!(
        strict::__moox_udf_native(1, 4, 1, &[3.0], &[&[1.0, 2.0, 3.0, 4.0]], &mut out),
        2
    );
    // 布局不一致（输入个数不对）。
    assert_eq!(strict::__moox_udf_native(1, 3, 1, &[3.0], &[], &mut out), 3);
    // 整数参数不是整数。
    assert_eq!(
        strict::__moox_udf_native(1, 3, 1, &[2.5], &[&[1.0, 2.0, 3.0]], &mut out),
        1
    );
}

#[test]
fn batches_of_series_are_independent() {
    let a = [1.0, 2.0, 3.0, 4.0];
    let b = [10.0, 20.0, 30.0, 40.0];
    let flat: Vec<f64> = a.iter().chain(b.iter()).copied().collect();
    let mut out = vec![0.0; 4];
    let rc = strict::__moox_udf_native(2, 4, 2, &[3.0, 3.0], &[&flat], &mut out);
    assert_eq!(rc, 0);
    assert_eq!(out, vec![6.0, 9.0, 60.0, 90.0]);
}

#[test]
fn window_accessors() {
    let data = [1.0, 2.0, 3.0];
    let w = Window::new(&data);
    assert_eq!((w.len(), w.first(), w.last(), w.get(1)), (3, 1.0, 3.0, 2.0));
    assert_eq!(w.iter().sum::<f64>(), 6.0);
    assert_eq!(w.as_slice(), &data);
    assert!(!w.is_empty());
}

#[test]
#[should_panic]
fn window_out_of_range_panics() {
    let data = [1.0, 2.0, 3.0];
    Window::new(&data).get(3);
}

mod finiteness {
    use super::*;

    #[udf(name = "inf_out", inputs = [x], params = { n: int(1..=3) }, lookback = "n")]
    fn inf_out(x: &Window, p: &InfOutParams) -> f64 {
        let _ = p;
        if x.last() > 2.0 {
            f64::INFINITY
        } else {
            x.last()
        }
    }

    #[test]
    fn infinity_becomes_nan() {
        let mut out = vec![0.0; 3];
        assert_eq!(
            __moox_udf_native(1, 3, 3, &[1.0], &[&[1.0, 2.0, 3.0]], &mut out),
            0
        );
        assert_eq!(out[0], 1.0);
        assert!(out[2].is_nan());
    }
}

mod shadowing {
    use super::*;

    // 用户的参数名可以叫 v、w、p：宏内部的变量名不能与它们冲突。
    #[udf(name = "shadow_check", inputs = [x], params = { v: int(1..=2), w: int(1..=3), p: float(0.0..=1.0) }, lookback = "w")]
    fn shadow_check(x: &Window, params: &ShadowCheckParams) -> f64 {
        x.last() * params.v as f64 + params.p
    }

    #[test]
    fn user_params_named_like_internals_compile_and_parse_in_order() {
        let mut out = vec![0.0; 1];
        assert_eq!(
            __moox_udf_native(1, 3, 1, &[2.0, 3.0, 0.5], &[&[1.0, 2.0, 3.0]], &mut out),
            0
        );
        assert_eq!(out[0], 3.0 * 2.0 + 0.5);
    }
}
