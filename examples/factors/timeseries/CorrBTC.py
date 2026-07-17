extra_data_dict = {
    "coin-btc": ["btc_close"],
}


def signal(df, n, factor_name):
    n = int(n)
    if "btc_close" in df.columns:
        benchmark_return = df["btc_close"].pct_change().fillna(0)
    else:
        benchmark_return = df["close"] * 0
    asset_return = df["close"].pct_change()
    df[factor_name] = asset_return.rolling(n, min_periods=1).corr(benchmark_return)
    return df
