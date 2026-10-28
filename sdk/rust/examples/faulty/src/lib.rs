//! 故障示例：能通过全部门禁（合成数据里没有哨兵值），遇到哨兵值才出错，用来在线上演练“只让这一次计算失败”：
//! - 窗口里出现 -7777777.0：死循环（宿主按截止时间中断）；
//! - 窗口里出现 -8888888.0：panic（abort，宿主只看到 trap）；
//! - 窗口里出现 -9999999.0：申请大量内存（触发实例内存上限）。
//!
//! 没有哨兵值时等于窗口均值。
#![deny(unsafe_code)]

use moox_udf::prelude::*;

#[udf(
    name = "faulty_mean",
    inputs = [x],
    params = { n: int(1..=1000) },
    lookback = "n",
)]
fn faulty_mean(x: &Window, p: &FaultyMeanParams) -> f64 {
    let _ = p;
    for v in x.iter() {
        if v == -7777777.0 {
            let mut i = 0u64;
            loop {
                i = std::hint::black_box(i.wrapping_add(1));
            }
        }
        if v == -8888888.0 {
            panic!("哨兵值触发的 panic");
        }
        if v == -9999999.0 {
            let big = std::hint::black_box(vec![1u8; 1 << 30]);
            return big[big.len() - 1] as f64;
        }
    }
    x.iter().sum::<f64>() / x.len() as f64
}
