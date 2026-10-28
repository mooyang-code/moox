//! 在原生环境下运行用户函数与 fixtures，便于调试与单元测试。门禁以 wasm 产物为准，原生测试只是辅助。
//!
//! fixtures 是目录里的若干 `*.json`，格式（NaN 写成 null）：
//!
//! ```json
//! {"cases": [{"name": "基础", "params": [3], "inputs": [[1, 2, 3, 4, 5]], "expected": [2, 3, 4], "rel_tol": 0}]}
//! ```
//!
//! 每个 case 是一条序列：`inputs` 的长度是 `expected` 的长度加窗口长度减一；`rel_tol` 为 0（默认）时要求逐位一致，
//! 否则允许相对误差。

use serde_json::Value;
use std::path::Path;

fn num(v: &Value) -> f64 {
    match v {
        Value::Null => f64::NAN,
        other => other
            .as_f64()
            .unwrap_or_else(|| panic!("fixtures 里的数值不合法：{other}")),
    }
}

fn series(v: &Value, what: &str) -> Vec<f64> {
    v.as_array()
        .unwrap_or_else(|| panic!("fixtures 的 {what} 要是数组"))
        .iter()
        .map(num)
        .collect()
}

type Native = fn(usize, usize, usize, &[f64], &[&[f64]], &mut [f64]) -> i32;

/// 运行目录下全部 fixtures，任何不一致都 panic 并给出位置。
pub fn run_fixtures(dir: &str, native: Native) {
    let mut files: Vec<_> = std::fs::read_dir(Path::new(dir))
        .unwrap_or_else(|e| panic!("读取 fixtures 目录 {dir}：{e}"))
        .filter_map(|e| e.ok())
        .map(|e| e.path())
        .filter(|p| p.extension().map(|x| x == "json").unwrap_or(false))
        .collect();
    files.sort();
    assert!(!files.is_empty(), "{dir} 里没有 fixtures");
    for file in files {
        let doc: Value = serde_json::from_slice(&std::fs::read(&file).unwrap())
            .unwrap_or_else(|e| panic!("{}：{e}", file.display()));
        for case in doc["cases"]
            .as_array()
            .unwrap_or_else(|| panic!("{} 缺少 cases", file.display()))
        {
            let name = case["name"].as_str().unwrap_or("?");
            let params = series(&case["params"], "params");
            let inputs: Vec<Vec<f64>> = case["inputs"]
                .as_array()
                .expect("inputs 要是数组")
                .iter()
                .map(|x| series(x, "inputs"))
                .collect();
            let expected = series(&case["expected"], "expected");
            let tol = case["rel_tol"].as_f64().unwrap_or(0.0);
            let in_len = inputs[0].len();
            let out_len = expected.len();
            let refs: Vec<&[f64]> = inputs.iter().map(|v| v.as_slice()).collect();
            let mut out = vec![0.0; out_len];
            let rc = native(1, in_len, out_len, &params, &refs, &mut out);
            assert_eq!(rc, 0, "{} / {name}：返回码 {rc}", file.display());
            for (i, (got, want)) in out.iter().zip(expected.iter()).enumerate() {
                let same = if got.is_nan() || want.is_nan() {
                    got.is_nan() && want.is_nan()
                } else if tol == 0.0 {
                    got.to_bits() == want.to_bits()
                } else {
                    (got - want).abs() <= tol * want.abs().max(f64::MIN_POSITIVE)
                };
                assert!(
                    same,
                    "{} / {name}：第 {i} 个输出 {got:?} 与期望 {want:?} 不一致",
                    file.display()
                );
            }
        }
    }
}
