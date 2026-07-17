def signal(df, n, factor_name):
    n = int(n)
    previous_close = df["close"].shift(1)
    up_volume = df["volume"].where(df["close"] > previous_close, 0)
    down_volume = df["volume"].where(df["close"] < previous_close, 0)
    up_sum = up_volume.rolling(n, min_periods=1).sum()
    down_sum = down_volume.rolling(n, min_periods=1).sum()
    df[factor_name] = up_sum / (1e-9 + down_sum)
    return df
