def signal(df, n, factor_name):
    n = int(n)
    close = df["close"]
    mean = close.rolling(n, min_periods=1).mean()
    standard_deviation = close.rolling(n, min_periods=1).std()
    zscore = (close - mean) / standard_deviation
    absolute_mean = zscore.abs().rolling(n, min_periods=1).mean()
    df[factor_name] = absolute_mean.rolling(n, min_periods=1).rank(
        ascending=True,
        pct=True,
    )
    return df
