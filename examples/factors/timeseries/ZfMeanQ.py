def signal(df, n, factor_name):
    n = int(n)
    amplitude = (df["high"] - df["low"]) / df["open"]
    mean = amplitude.rolling(n, min_periods=1).mean()
    df[factor_name] = mean.rolling(n, min_periods=1).rank(ascending=True, pct=True)
    return df
