//! moox 因子自定义算子（wasm UDF）的 Rust SDK。
//!
//! 用户只写算法：[`udf`] 宏生成 ABI 导出、参数结构体、逐点循环、空值规则与元数据；
//! 窗口通过 [`Window`] 交给用户函数，越界访问即 panic（panic 即 abort，宿主只看到 trap）。

pub mod exact;
pub mod rt;
#[cfg(feature = "testing")]
pub mod testing;
mod window;

pub use moox_udf_macros::udf;
pub use window::Window;

/// 常用导入：`use moox_udf::prelude::*;`
pub mod prelude {
    pub use crate::udf;
    pub use crate::Window;
}
