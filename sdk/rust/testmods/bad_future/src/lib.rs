//! 不合格样本：读窗口之后的数据（违反只依赖窗口，且读到了未来）。
#![allow(unsafe_code)]

use moox_udf::prelude::*;

#[udf(name = "bad_future", inputs = [x], params = { n: int(2..=20) }, lookback = "n")]
fn bad_future(x: &Window, p: &BadFutureParams) -> f64 {
    let _ = p;
    let after = unsafe { *x.as_slice().as_ptr().add(x.len()) };
    x.last() + after
}
