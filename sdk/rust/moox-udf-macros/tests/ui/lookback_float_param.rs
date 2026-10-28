use moox_udf::prelude::*;
#[udf(name = "f", inputs = [x], params = { q: float(0.0..=1.0) }, lookback = "q")]
fn f(x: &Window, p: &FParams) -> f64 { let _ = p; x.last() }
fn main() {}
