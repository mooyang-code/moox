def compute(df, params):
    close = df["close"]
    outputs = {}
    for window in params["windows"]:
        average = close.rolling(window, min_periods=1).mean()
        outputs[f"bias_{window}"] = close / average - 1
    return outputs
