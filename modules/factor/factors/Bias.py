def signal_multi_params(df, param_list):
    close = df["close"]
    out = {}
    for n in param_list:
        n = int(n)
        ma = close.rolling(n, min_periods=1).mean()
        out[str(n)] = close / ma
    return out
