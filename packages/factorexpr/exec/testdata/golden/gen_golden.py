#!/usr/bin/env python3
"""生成 factorexpr 的 pandas 对照数据 golden.json（开发工具，CI 不运行）。

用法（仓库根目录，需要 numpy 与 pandas）：
    python3 packages/factorexpr/exec/testdata/golden/gen_golden.py

内容：
- rolling：各时序算子在一条序列上的结果，pandas 严格窗口（min_periods = n）；
- cross：各截面算子在 12 个标的 × 30 个时刻上的结果（按时刻与 series_tag 分组）。

约定：结果里的 NaN 与 ±Inf 写成 null（与 factorexpr 把非有限值记为 NaN 一致）。
"""
import json
import math
import pathlib

import numpy as np
import pandas as pd

HERE = pathlib.Path(__file__).resolve()
OUT = HERE.with_name("golden.json")
rng = np.random.default_rng(20261017)


def clean(values):
    out = []
    for v in np.asarray(values, dtype=float):
        out.append(None if not math.isfinite(v) else float(v))
    return out


# ---------------------------------------------------------------- 时序算子
N = 400
x = 100 * np.exp(np.cumsum(rng.normal(0, 0.01, N)))
y = 50 + np.cumsum(rng.normal(0, 0.5, N))
x[60:90] = x[60]  # 30 根常数（冷门币价格不动）
x[150:170] = np.round(x[150:170])  # 取整制造并列值
y[100:130] = np.round(y[100:130])
x[360] = np.nan  # 窗口内的 NaN
y[300] = np.nan
xs, ys = pd.Series(x), pd.Series(y)
cond = np.where(np.isnan(x), np.nan, (x > np.nanmedian(x)).astype(float))  # 严格三值：NaN 比较为 NaN

rolling = []
# pandas 的滚动方差类算子用在线增删（Welford）算法，价格量级、小窗口时有约 1e−8 的相对误差（例如 2 根窗口的 zscore
# 精确值是 ±1/√2，pandas 会偏 3e−8）；factorexpr 用精确求和的两遍法，精度另由高精度参考实现按 1e−12 验证。
MOMENT_TOL = 1e-7


def add(expression, values, tol=1e-12):
    rolling.append({"expr": expression, "tol": tol, "expect": clean(values)})


def strict_apply(series, n, fn):
    return series.rolling(n, min_periods=n).apply(fn, raw=True)


for n in (2, 5, 20, 60):
    r = xs.rolling(n, min_periods=n)
    add(f"ts_sum(x, {n})", r.sum())
    add(f"ts_mean(x, {n})", r.mean())
    add(f"ts_min(x, {n})", r.min())
    add(f"ts_max(x, {n})", r.max())
    add(f"ts_std(x, {n})", r.std(), MOMENT_TOL)
    add(f"ts_var(x, {n})", r.var(), MOMENT_TOL)
    add(f"ts_zscore(x, {n})", (xs - r.mean()) / r.std(), MOMENT_TOL)
    add(f"ts_rank(x, {n})", r.rank(pct=True))
    add(f"ts_median(x, {n})", r.median())
    add(f"ts_product(x / 100, {n})", strict_apply(xs / 100, n, np.prod), 1e-12)
    add(f"ts_argmax(x, {n})", strict_apply(xs, n, lambda w: len(w) - 1 - np.argmax(w)))
    add(f"ts_argmin(x, {n})", strict_apply(xs, n, lambda w: len(w) - 1 - np.argmin(w)))
    add(f"ts_decay_linear(x, {n})", strict_apply(xs, n, lambda w: np.dot(w, np.arange(1, len(w) + 1)) / (len(w) * (len(w) + 1) / 2)))
    add(f"ts_count(x > {float(np.nanmedian(x))!r}, {n})", pd.Series(cond).rolling(n, min_periods=n).sum())
    add(f"ts_corr(x, y, {n})", xs.rolling(n, min_periods=n).corr(ys), MOMENT_TOL)
    add(f"ts_cov(x, y, {n})", xs.rolling(n, min_periods=n).cov(ys), MOMENT_TOL)
    for q in (0, 0.25, 0.5, 0.9, 1):
        add(f"ts_quantile(x, {n}, {q})", r.quantile(q))

    def slope(w):
        return np.polyfit(np.arange(1, len(w) + 1), w, 1)[0]

    def rsquare(w):
        if np.all(w == w[0]):
            return np.nan
        return np.corrcoef(np.arange(1, len(w) + 1), w)[0, 1] ** 2

    def resi(w):
        b, a = np.polyfit(np.arange(1, len(w) + 1), w, 1)
        return w[-1] - (a + b * len(w))

    add(f"ts_slope(x, {n})", strict_apply(xs, n, slope), 1e-8)
    add(f"ts_rsquare(x, {n})", strict_apply(xs, n, rsquare), 1e-8)
    add(f"ts_resi(x, {n})", strict_apply(xs, n, resi), 1e-8)

for n in (5, 20, 60):
    r = xs.rolling(n, min_periods=n)
    # 常数窗口的偏度、峰度没有定义：pandas 返回 0 与 −3，factorexpr 按“退化窗口为 null”记为 NaN（设计 4.4）。
    constant = (r.max() == r.min()).to_numpy()
    add(f"ts_skew(x, {n})", np.where(constant, np.nan, r.skew()), MOMENT_TOL)
    add(f"ts_kurt(x, {n})", np.where(constant, np.nan, r.kurt()), MOMENT_TOL)

for k in (1, 3):
    add(f"delay(x, {k})", xs.shift(k))
    add(f"delta(x, {k})", xs - xs.shift(k))
    add(f"pct_change(x, {k})", xs / xs.shift(k) - 1)


def truncated(values, alpha):
    k = math.ceil(math.log(1e-9) / math.log(1 - alpha)) if alpha < 1 else 1
    weights = np.empty(k)
    decay = 1.0
    for j in range(k - 1):
        weights[k - 1 - j] = alpha * decay
        decay *= 1 - alpha
    weights[0] = decay
    out = np.full(len(values), np.nan)
    for t in range(k - 1, len(values)):
        window = values[t - k + 1 : t + 1]
        if not np.isnan(window).any():
            out[t] = np.dot(weights, window)
    return out, k


for expression, alpha in (("ema(x, 5)", 2 / 6), ("ema(x, 20)", 2 / 21), ("sma(x, 10, 1)", 0.1)):
    expected, k = truncated(x, alpha)
    # 截断定义与 pandas 从头递推的 ewm(adjust=False) 相差不超过被截掉部分的权重（相对 1e−9 量级）。
    reference = pd.Series(x[:360]).ewm(alpha=alpha, adjust=False).mean().to_numpy()
    diff = np.abs(expected[k - 1 : 360] - reference[k - 1 :]) / np.abs(reference[k - 1 :])
    assert diff.max() < 1e-8, (expression, diff.max())
    add(expression, expected, 1e-12)

# ---------------------------------------------------------------- 截面算子
S, T = 12, 30
tags = ["venue:a"] * 7 + ["venue:b"] * 5
subjects = [f"S{i:02d}" for i in range(S)]
v = rng.normal(0, 1, (S, T))
v[0:4, 5] = 1.0  # 并列
v[7:12, 8] = 2.5  # venue:b 整组相同：zscore 为 NaN
v[3, 10] = np.nan
v[7:11, 12] = np.nan  # venue:b 只剩一个有效值
frame = pd.DataFrame(
    [(t, tags[s], subjects[s], v[s, t]) for s in range(S) for t in range(T)],
    columns=["time", "tag", "subject", "v"],
)
group = frame.groupby(["time", "tag"])["v"]
mean = group.transform("mean")
std = group.transform("std")
count = group.transform("count")
cross_cases = {
    "cs_rank(v)": group.rank(pct=True),
    "cs_zscore(v)": ((frame["v"] - mean) / std).where(count >= 2),
    "cs_demean(v)": frame["v"] - mean,
    "cs_scale(v)": frame["v"] / group.transform(lambda s: s.abs().sum()),
}
cross = {
    "subjects": subjects,
    "tags": tags,
    "times": T,
    "v": [clean(v[s]) for s in range(S)],
    "cases": [],
}
for expression, values in cross_cases.items():
    matrix = values.to_numpy().reshape(S, T)
    cross["cases"].append({"expr": expression, "tol": 1e-12, "expect": [clean(matrix[s]) for s in range(S)]})

OUT.write_text(json.dumps({"pandas": pd.__version__, "numpy": np.__version__, "length": N,
                           "x": clean(x), "y": clean(y), "rolling": rolling, "cross": cross},
                          separators=(",", ":")) + "\n")
print(f"wrote {OUT} ({OUT.stat().st_size} bytes): {len(rolling)} rolling cases, {len(cross['cases'])} cross cases")
