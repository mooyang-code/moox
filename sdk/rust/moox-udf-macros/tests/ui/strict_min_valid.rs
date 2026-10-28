use moox_udf::prelude::*;
#[udf(name = "f", inputs = [x], params = { n: int(1..=5) }, lookback = "n", min_valid = 3)]
fn f(x: &Window, p: &FParams) -> f64 { let _ = p; x.last() }
fn main() {}
