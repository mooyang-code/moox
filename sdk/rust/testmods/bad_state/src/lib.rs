//! 不合格样本：带全局可变状态，输出依赖之前的调用（违反“只依赖窗口”）。
#![allow(unsafe_code)]

use moox_udf::prelude::*;
use std::cell::Cell;

struct Counter(Cell<f64>);
unsafe impl Sync for Counter {}
static COUNTER: Counter = Counter(Cell::new(0.0));

#[udf(name = "bad_state", inputs = [x], params = { n: int(2..=20) }, lookback = "n")]
fn bad_state(x: &Window, p: &BadStateParams) -> f64 {
    let _ = p;
    COUNTER.0.set(COUNTER.0.get() + 1.0);
    x.last() + COUNTER.0.get()
}
