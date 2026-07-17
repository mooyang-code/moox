def signal(df, n, factor_name):
    n = int(n)
    typical = (df["high"] + df["low"] + df["close"]) / 3
    minimum = typical.rolling(n, min_periods=1).min()
    maximum = typical.rolling(n, min_periods=1).max()
    df[factor_name] = (typical - minimum) / (maximum - minimum) - 0.5
    return df
