def compute(df, params):
    window = int(params.get("window", 1))
    grouped = df.groupby("series_tag", sort=False)
    excess_return = grouped["nav"].pct_change() - df["benchmark_return"]
    rolling_rank = excess_return.groupby(df["series_tag"], sort=False).transform(
        lambda values: values.rolling(window, min_periods=1).rank(pct=True)
    )
    output = df[["data_time", "series_tag"]].copy()
    output["excess_return"] = excess_return
    output["rolling_rank"] = rolling_rank
    return output
