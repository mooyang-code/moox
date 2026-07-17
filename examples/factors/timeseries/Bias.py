def signal(df, n, factor_name):
    n = int(n)
    df[factor_name] = df["close"] / df["close"].rolling(n, min_periods=1).mean()
    return df


def signal_multi_params(df, param_list):
    close = df["close"]
    result = {}
    for param in param_list:
        n = int(param)
        result[str(param)] = close / close.rolling(n, min_periods=1).mean()
    return result
