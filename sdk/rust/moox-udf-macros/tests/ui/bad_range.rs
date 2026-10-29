use moox_udf::prelude::*;
#[udf(name = "f", inputs = [x], params = { n: int(5..=1) }, lookback = "n")]
fn f(x: &Window, p: &FParams) -> f64 { let _ = p; x.last() }
fn main() {}
