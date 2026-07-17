def signal(df, n, factor_name):
    n = int(n)
    bias = df["close"] / df["close"].rolling(n, min_periods=1).mean() - 1
    df[factor_name] = bias.rolling(n, min_periods=1).rank(ascending=True, pct=True)
    return df
