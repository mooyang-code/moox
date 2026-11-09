#!/usr/bin/env python3
"""生成 factorexpr 的 pandas 对照数据 golden.json（开发工具，CI 不运行）。

用法（仓库根目录，需要 numpy 与 pandas）：
    python3 packages/factorexpr/exec/testdata/golden/gen_golden.py

内容：
- rolling：各时序算子在一条序列上的结果，pandas 严格窗口（min_periods = n）；宽松窗口算子在开头带 NaN 的序列 z、u 上，
  pandas rolling(n, min_periods=m)；
- cross：各截面算子在 12 个标的 × 30 个时刻上的结果（按时刻与 series_tag 分组）；逐行变换类用 v，
  聚合广播类用 w（在 v 上挖出整组为 NaN 的截面）；多输入、分组截面与截面回归用 a、b、y2、amount 与分组键 g，
  回归的期望值由 numpy lstsq 在完整设计矩阵（截距或分组哑变量）上算出，误差 ≤ 1e-9。

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

# ---------------------------------------------------------------- 宽松窗口算子
# 另用一个随机数发生器生成 z、u，不影响上面 x、y 与下面截面数据的取值。z、u 开头分别有 25、37 根 NaN（序列还没开始），
# 之后没有 NaN：pandas rolling(n, min_periods=m) 在这种序列上与宽松窗口的定义一致（pandas 会跳过中途的 NaN，宽松窗口不跳过，
# 所以中途 NaN 的口径由 ops 的单元测试覆盖）。两个序列参数时 pandas 只用两边都有效的行，等于共同的有效尾段。
relaxed_rng = np.random.default_rng(20261108)
z = 100 * np.exp(np.cumsum(relaxed_rng.normal(0, 0.01, N)))
u = 50 + np.cumsum(relaxed_rng.normal(0, 0.5, N))
z[:25] = np.nan
u[:37] = np.nan
zs, us = pd.Series(z), pd.Series(u)
zcond = np.where(np.isnan(z), np.nan, (z > np.nanmedian(z)).astype(float))


def relaxed_apply(series, n, m, fn):
    return series.rolling(n, min_periods=m).apply(lambda w: fn(w[~np.isnan(w)]), raw=True)


for n, m in ((5, 1), (5, 3), (20, 4), (20, 20)):
    r = zs.rolling(n, min_periods=m)
    m2, m3, m4 = max(m, 2), max(m, 3), max(m, 4)
    add(f"ts_sum_relaxed(z, {n}, {m})", r.sum())
    add(f"ts_median_relaxed(z, {n}, {m})", r.median())
    add(f"ts_rank_relaxed(z, {n}, {m})", r.rank(pct=True))
    add(f"ts_product_relaxed(z / 100, {n}, {m})", relaxed_apply(zs / 100, n, m, np.prod))
    add(f"ts_argmax_relaxed(z, {n}, {m})", relaxed_apply(zs, n, m, lambda w: len(w) - 1 - np.argmax(w)))
    add(f"ts_argmin_relaxed(z, {n}, {m})", relaxed_apply(zs, n, m, lambda w: len(w) - 1 - np.argmin(w)))
    add(f"ts_count_relaxed(z > {float(np.nanmedian(z))!r}, {n}, {m})", pd.Series(zcond).rolling(n, min_periods=m).sum())
    for q in (0, 0.3, 1):
        add(f"ts_quantile_relaxed(z, {n}, {q}, {m})", r.quantile(q))
    r2 = zs.rolling(n, min_periods=m2)
    add(f"ts_var_relaxed(z, {n}, {m2})", r2.var(), MOMENT_TOL)
    add(f"ts_zscore_relaxed(z, {n}, {m2})", (zs - r2.mean()) / r2.std(), MOMENT_TOL)
    add(f"ts_corr_relaxed(z, u, {n}, {m2})", zs.rolling(n, min_periods=m2).corr(us), MOMENT_TOL)
    add(f"ts_cov_relaxed(z, u, {n}, {m2})", zs.rolling(n, min_periods=m2).cov(us), MOMENT_TOL)
    add(f"ts_skew_relaxed(z, {n}, {m3})", zs.rolling(n, min_periods=m3).skew(), MOMENT_TOL)
    add(f"ts_kurt_relaxed(z, {n}, {m4})", zs.rolling(n, min_periods=m4).kurt(), MOMENT_TOL)

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

# 聚合广播类截面函数用 w：在 v 上再挖出整组为 NaN 的截面（不调用 rng，v 与上面的期望值不变）。
w = v.copy()
w[7:12, 15] = np.nan  # venue:b 整组 NaN
w[0:7, 20] = np.nan  # venue:a 整组 NaN
w[1:6, 22] = np.nan  # venue:a 只剩 S00、S06 两个有效值
wframe = frame.assign(w=[w[s, t] for s in range(S) for t in range(T)])
wgroup = wframe.groupby(["time", "tag"])["w"]
# 布尔条件保留 NaN（严格三值）：w 为 NaN 的行条件也是 NaN，不计数。
wframe["c"] = np.where(np.isnan(wframe["w"]), np.nan, (wframe["w"] > 0).astype(float))
cgroup = wframe.groupby(["time", "tag"])["c"]


def reference(subject):
    ref = wframe[wframe["subject"] == subject].set_index(["time", "tag"])["w"]
    keys = list(zip(wframe["time"], wframe["tag"]))
    return pd.Series([ref.get(key, np.nan) for key in keys], index=wframe.index)


wmedian = wgroup.transform("median")
masked = wframe["w"].where(wframe["w"] > 0)  # where(w > 0, w, nan)
aggregate_cases = {
    "cs_mean(w)": wgroup.transform("mean"),
    "cs_sum(w)": wgroup.transform(lambda s: s.sum(min_count=1)),  # 全 NaN 为 NaN，不是 0
    "cs_count(w > 0)": cgroup.transform(lambda s: s.sum(min_count=1)),
    "cs_std(w)": wgroup.transform("std"),
    "cs_median(w)": wmedian,
    "cs_min(w)": wgroup.transform("min"),
    "cs_max(w)": wgroup.transform("max"),
    'cs_ref(w, "S03")': reference("S03"),
    'cs_ref(w, "S09")': reference("S09"),
    "abs(w - cs_median(w))": (wframe["w"] - wmedian).abs(),
    "cs_rank(where(w > 0, w, nan))": masked.groupby([wframe["time"], wframe["tag"]]).rank(pct=True),
    "w - cs_mean(w)": wframe["w"] - wgroup.transform("mean"),
}
for q in (0, 0.25, 0.9, 1):
    aggregate_cases[f"cs_quantile(w, {q})"] = wgroup.transform(lambda s, q=q: s.quantile(q))

# 多输入与分组截面函数（第 2 期）：另用一个随机数发生器生成 a、b、y2 与分组键 g，不影响上面的数据与期望值。
# 分组键 g 取 0～2（部分行为 NaN，不属于任何组）；回归用例里另有一个截面让 b = 2a（奇异），一个截面让 a 在组内为常数（与哑变量共线）。
group_rng = np.random.default_rng(20261109)
a = group_rng.normal(0, 1, (S, T))
b = group_rng.normal(0, 1, (S, T))
y2 = 0.5 * a - 0.3 * b + group_rng.normal(0, 0.5, (S, T))
g = group_rng.integers(0, 3, (S, T)).astype(float)
amount = np.abs(group_rng.normal(10, 4, (S, T)))
a[2, 3] = np.nan
b[8, 4] = np.nan
y2[5, 6] = np.nan
g[1, 7] = np.nan
g[9, 7] = np.nan
g[0:7, 9] = np.nan  # venue:a 整截面没有分组键
amount[3, 11] = np.nan
amount[0:3, 13] = 7.0  # 并列（按标的排序）
amount[7:12, 14] = 0.0  # venue:b 总和为 0
amount[4, 16] = -1.0  # w ≤ 0 的行不参与加权
b[:, 18] = 2 * a[:, 18]  # 奇异：b 与 a 共线
for s in range(S):
    a[s, 19] = 1.0 + (g[s, 19] if not np.isnan(g[s, 19]) else 0)  # a 在组内为常数：与哑变量共线
a[7:12, 21] = 3.0  # venue:b 的 a 是常数：与截距共线
a[0:7, 23] = np.nan  # venue:a 没有有效的 a
gframe = frame[["time", "tag", "subject"]].copy()
for name, matrix in (("a", a), ("b", b), ("y2", y2), ("g", g), ("amount", amount)):
    gframe[name] = [matrix[s, t] for s in range(S) for t in range(T)]
gframe["c"] = np.where(np.isnan(gframe["a"]), np.nan, (gframe["a"] > 0).astype(float))


def by_section(fn):
    """按 (time, tag) 截面逐个计算：fn 收到截面的子表，返回与它等长的结果。"""
    out = pd.Series(np.nan, index=gframe.index)
    for _, part in gframe.groupby(["time", "tag"]):
        out.loc[part.index] = np.asarray(fn(part), dtype=float)
    return out


def by_group(fn):
    """截面内再按 g 分组（g 为 NaN 的行输出 NaN）：fn 收到组的子表，返回与它等长的结果。"""
    def section(part):
        out = pd.Series(np.nan, index=part.index)
        for _, members in part[part["g"].notna()].groupby("g"):
            out.loc[members.index] = np.asarray(fn(members), dtype=float)
        return out
    return by_section(section)


def broadcast(fn):
    return lambda members: np.full(len(members), fn(members))


def rank_pct(values):
    return pd.Series(values).rank(pct=True).to_numpy()


def zscore(values):
    values = pd.Series(values)
    if values.count() < 2:
        return np.full(len(values), np.nan)
    return ((values - values.mean()) / values.std()).to_numpy()


def corr(part):
    valid = part[["a", "y2"]].dropna()
    if len(valid) < 3:
        return np.nan
    return valid["a"].corr(valid["y2"])


def wmean(members):
    use = members[members["a"].notna() & members["amount"].notna() & (members["amount"] > 0)]
    if len(use) == 0:
        return np.nan
    return (use["a"] * use["amount"]).sum() / use["amount"].sum()


def cumshare(members):
    valid = members[members["amount"].notna()].copy()
    out = pd.Series(np.nan, index=members.index)
    if len(valid) == 0:
        return out
    valid = valid.assign(neg=-valid["amount"]).sort_values(["neg", "subject"])
    total = valid["amount"].sum()
    if total == 0:
        return out
    out.loc[valid.index] = valid["amount"].cumsum() / total
    return out


def across(fn):
    """组间：每组取 v 的有效均值，在组之间排名或标准化，再广播回组内。"""
    def section(part):
        out = pd.Series(np.nan, index=part.index)
        keyed = part[part["g"].notna()]
        if len(keyed) == 0:
            return out
        means = keyed.groupby("g")["a"].mean()
        scores = pd.Series(fn(means.to_numpy()), index=means.index)
        out.loc[keyed.index] = keyed["g"].map(scores).to_numpy()
        return out
    return by_section(section)


def resid(columns, grouped):
    """numpy lstsq 参考：完整设计矩阵（截距，或每组一个哑变量）；有效行数不超过参数个数或秩不足时整截面为 NaN。"""
    def section(part):
        need = ["y2"] + columns + (["g"] if grouped else [])
        valid = part.dropna(subset=need)
        out = pd.Series(np.nan, index=part.index)
        if len(valid) == 0:
            return out
        if grouped:
            dummies = pd.get_dummies(valid["g"]).to_numpy(dtype=float)
        else:
            dummies = np.ones((len(valid), 1))
        design = np.column_stack([dummies] + [valid[c].to_numpy() for c in columns])
        if len(valid) <= design.shape[1] or np.linalg.matrix_rank(design) < design.shape[1]:
            return out
        beta, *_ = np.linalg.lstsq(design, valid["y2"].to_numpy(), rcond=None)
        out.loc[valid.index] = valid["y2"].to_numpy() - design @ beta
        return out
    return by_section(section)


group_cases = {
    "cs_corr(a, y2)": by_section(lambda part: np.full(len(part), corr(part))),
    "gs_rank(a, g)": by_group(lambda m: rank_pct(m["a"].to_numpy())),
    "gs_demean(a, g)": by_group(lambda m: (m["a"] - m["a"].mean()).to_numpy()),
    "gs_zscore(a, g)": by_group(lambda m: zscore(m["a"].to_numpy())),
    "gs_cumshare(amount, g)": by_group(cumshare),
    "gs_mean(a, g)": by_group(broadcast(lambda m: m["a"].mean())),
    "gs_sum(a, g)": by_group(broadcast(lambda m: m["a"].sum(min_count=1))),
    "gs_count(a > 0, g)": by_group(broadcast(lambda m: m["c"].sum(min_count=1))),
    "gs_median(a, g)": by_group(broadcast(lambda m: m["a"].median())),
    "gs_wmean(a, amount, g)": by_group(broadcast(wmean)),
    "gx_rank(a, g)": across(rank_pct),
    "gx_zscore(a, g)": across(zscore),
}
resid_cases = {
    "cs_resid(y2, a)": resid(["a"], False),
    "cs_resid(y2, a, b)": resid(["a", "b"], False),
    "gs_resid(y2, g)": resid([], True),
    "gs_resid(y2, g, a)": resid(["a"], True),
    "gs_resid(y2, g, a, b)": resid(["a", "b"], True),
}

cross = {
    "subjects": subjects,
    "tags": tags,
    "times": T,
    "v": [clean(v[s]) for s in range(S)],
    "w": [clean(w[s]) for s in range(S)],
    "cases": [],
}
for expression, values in list(cross_cases.items()) + list(aggregate_cases.items()):
    matrix = values.to_numpy().reshape(S, T)
    cross["cases"].append({"expr": expression, "tol": 1e-12, "expect": [clean(matrix[s]) for s in range(S)]})
for name, matrix in (("a", a), ("b", b), ("y2", y2), ("g", g), ("amount", amount)):
    cross[name] = [clean(matrix[s]) for s in range(S)]
for expression, values in group_cases.items():
    matrix = values.to_numpy().reshape(S, T)
    cross["cases"].append({"expr": expression, "tol": 1e-12, "expect": [clean(matrix[s]) for s in range(S)]})
for expression, values in resid_cases.items():
    matrix = values.to_numpy().reshape(S, T)
    cross["cases"].append({"expr": expression, "tol": 1e-9, "expect": [clean(matrix[s]) for s in range(S)]})

OUT.write_text(json.dumps({"pandas": pd.__version__, "numpy": np.__version__, "length": N,
                           "x": clean(x), "y": clean(y), "z": clean(z), "u": clean(u), "rolling": rolling, "cross": cross},
                          separators=(",", ":")) + "\n")
print(f"wrote {OUT} ({OUT.stat().st_size} bytes): {len(rolling)} rolling cases, {len(cross['cases'])} cross cases")
