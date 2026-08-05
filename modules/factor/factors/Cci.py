def compute(df, params):
    window = int(params["window"])
    typical = (df["high"] + df["low"] + df["close"]) / 3
    grouped = typical.groupby(df["series_tag"], sort=False)
    mean = grouped.transform(lambda values: values.rolling(window, min_periods=1).mean())
    deviation = (typical - mean).abs().groupby(df["series_tag"], sort=False).transform(
        lambda values: values.rolling(window, min_periods=1).mean()
    )
    output = df[["data_time", "series_tag"]].copy()
    output["cci"] = (typical - mean) / (0.015 * deviation)
    return output