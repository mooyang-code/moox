def compute(df, params):
    output = df[["data_time", "series_tag"]].copy()
    close = df.groupby("series_tag", sort=False)["close"]
    m = int(params.get("m", 1))
    for raw_window in params["windows"]:
        window = int(raw_window)
        if window < 1 or m < 1 or m > window:
            raise ValueError("SMA requires window >= 1 and 1 <= m <= window")
        output[f"sma_{window}"] = close.transform(
            lambda values: values.ewm(alpha=m / window, adjust=False).mean()
        )
    return output
