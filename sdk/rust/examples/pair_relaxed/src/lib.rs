//! 示例：两个输入、宽松空值（relaxed）的相关系数：序列开头的 NaN（还没开始）不影响计算，
//! 在两个输入共同的有效段上计算，有效段不足 min_valid 个点时输出 NaN。
#![deny(unsafe_code)]

use moox_udf::prelude::*;

#[udf(
    name = "pair_corr",
    inputs = [x, y],
    params = { n: int(2..=500) },
    lookback = "n",
    nan = "relaxed",
    min_valid = 2,
)]
fn pair_corr(x: &Window, y: &Window, p: &PairCorrParams) -> f64 {
    let _ = p;
    let n = x.len() as f64;
    let (mx, my) = (x.iter().sum::<f64>() / n, y.iter().sum::<f64>() / n);
    let (mut sxy, mut sxx, mut syy) = (0.0, 0.0, 0.0);
    for i in 0..x.len() {
        let (dx, dy) = (x.get(i) - mx, y.get(i) - my);
        sxy += dx * dy;
        sxx += dx * dx;
        syy += dy * dy;
    }
    let denom = (sxx * syy).sqrt();
    if denom == 0.0 {
        return f64::NAN;
    }
    sxy / denom
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
