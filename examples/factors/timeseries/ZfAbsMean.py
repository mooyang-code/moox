def signal(df, n, factor_name):
    n = int(n)
    average_price = (df["close"] + df["high"] + df["low"]) / 3
    price_return = average_price.pct_change()
    amplitude = (df["high"] - df["low"]) / df["open"]
    positive_amplitude = amplitude.where(price_return > 0, 0)
    amplitude_mean = positive_amplitude.rolling(n, min_periods=1).mean()
    df[factor_name] = amplitude_mean.rolling(n, min_periods=1).rank(
        ascending=True,
        pct=True,
    )
    return df
