def signal(df, n, factor_name):
    n = int(n)
    price_return = df["close"].pct_change()
    amplitude = (df["high"] - df["low"]) / df["open"]
    signed_amplitude = amplitude.where(price_return > 0, -amplitude)
    df[factor_name] = signed_amplitude.rolling(n, min_periods=1).std()
    return df
