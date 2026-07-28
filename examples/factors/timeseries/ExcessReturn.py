def compute(df, params):
    window = int(params.get("window", 1))
    excess_return = df["nav"].pct_change() - df["benchmark_return"]
    rolling_rank = excess_return.rolling(window, min_periods=1).rank(pct=True)
    return {
        "excess_return": excess_return,
        "rolling_rank": rolling_rank,
    }
