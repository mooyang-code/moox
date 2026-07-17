def signal(df, n, factor_name):
    n = int(n)
    mean = df["quote_volume"].rolling(n, min_periods=1).mean()
    df[factor_name] = mean.rolling(n, min_periods=1).rank(ascending=True, pct=True)
    return df
