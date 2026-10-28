//! 不合格样本：读窗口之前的数据（违反“回看如实”）。
#![allow(unsafe_code)]

use moox_udf::prelude::*;

#[udf(name = "bad_past", inputs = [x], params = { n: int(2..=20) }, lookback = "n")]
fn bad_past(x: &Window, p: &BadPastParams) -> f64 {
    let _ = p;
    // 越过窗口起点读前一个值。
    let before = unsafe { *x.as_slice().as_ptr().offset(-1) };
    x.last() + before
}
