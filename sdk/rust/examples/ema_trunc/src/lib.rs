//! 示例：截断到 k 根的指数移动平均，与 factorexpr 内置的 `ema(x, n, k)` 逐位一致：
//! y_t = (1−α)^(k−1)·x_{t−k+1} + Σ_{j=0}^{k−2} α(1−α)^j·x_{t−j}，α = 2/(n+1)，各项精确求和。
#![deny(unsafe_code)]

use moox_udf::exact::ExactSum;
use moox_udf::prelude::*;

#[udf(
    name = "ema_trunc",
    inputs = [x],
    params = { n: int(1..=5000), k: int(1..=60000) },
    lookback = "k",
)]
fn ema_trunc(x: &Window, p: &EmaTruncParams) -> f64 {
    let k = p.k as usize;
    let alpha = 2.0 / ((p.n + 1) as f64);
    let keep = 1.0 - alpha;
    let xs = x.as_slice();
    let mut acc = ExactSum::new();
    let mut decay = 1.0;
    // j = 0 是最新一根（窗口末尾）；最老的一根用 (1−α)^(k−1) 作权重。
    for j in 0..k - 1 {
        acc.add((alpha * decay) * xs[k - 1 - j]);
        decay *= keep;
    }
    acc.add(decay * xs[0]);
    acc.round()
}

#[cfg(test)]
mod tests {
    use super::*;
    use moox_udf::testing;

    #[test]
    fn fixtures() {
        testing::run_fixtures(
            concat!(env!("CARGO_MANIFEST_DIR"), "/tests/fixtures"),
            __moox_udf_native,
        );
    }
}
