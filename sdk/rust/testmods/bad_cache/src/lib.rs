//! 不合格样本：把第一次收到的参数缓存在全局变量里，之后一直用它（违反“只依赖窗口”，跨参数调用状态）。
//! 同一组参数下的每个测试都一样，所以只有在“同一个实例被不同参数复用”时才暴露。
#![allow(unsafe_code)]

use moox_udf::prelude::*;
use std::cell::Cell;

struct Cache(Cell<f64>);
unsafe impl Sync for Cache {}
static FIRST_Q: Cache = Cache(Cell::new(f64::NAN));

#[udf(name = "bad_cache", inputs = [x], params = { q: float(0.5..=2.0) }, lookback = "3")]
fn bad_cache(x: &Window, p: &BadCacheParams) -> f64 {
    if FIRST_Q.0.get().is_nan() {
        FIRST_Q.0.set(p.q);
    }
    x.last() * FIRST_Q.0.get()
}
