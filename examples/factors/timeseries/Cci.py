def compute(df, params):
    window = int(params["window"])
    typical = (df["high"] + df["low"] + df["close"]) / 3
    mean = typical.rolling(window, min_periods=1).mean()
    deviation = (typical - mean).abs().rolling(window, min_periods=1).mean()
    return {"cci": (typical - mean) / (0.015 * deviation)}
