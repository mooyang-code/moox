//! 示例：滑动窗口均值，与 factorexpr 内置的 `ts_mean` 逐位一致：窗口内的值全部相同时直接返回这个值，
//! 否则是精确和（四舍五入到最近的 f64）除以窗口长度。作为验收基准。
#![deny(unsafe_code)]

use moox_udf::exact::exact_sum;
use moox_udf::prelude::*;

#[udf(
    name = "ts_mean_udf",
    inputs = [x],
    params = { n: int(1..=5000) },
    lookback = "n",
)]
fn ts_mean_udf(x: &Window, p: &TsMeanUdfParams) -> f64 {
    let _ = p;
    let first = x.first();
    if x.iter().all(|v| v == first) {
        return first;
    }
    exact_sum(x.as_slice()) / x.len() as f64
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
