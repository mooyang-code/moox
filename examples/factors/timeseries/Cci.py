def signal(df, n, factor_name):
    n = int(n)
    typical = (df["high"] + df["low"] + df["close"]) / 3
    mean = typical.rolling(n, min_periods=1).mean()
    deviation = (typical - mean).abs().rolling(n, min_periods=1).mean()
    df[factor_name] = (typical - mean) / (0.015 * deviation)
    return df
