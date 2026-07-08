def signal(*args):
    df = args[0]
    n = int(args[1])
    factor_name = args[2]

    typical = (df["high"] + df["low"] + df["close"]) / 3
    mean = typical.rolling(n, min_periods=1).mean()
    deviation = (typical - mean).abs().rolling(n, min_periods=1).mean()
    df[factor_name] = (typical - mean) / (0.015 * deviation)
    return df
