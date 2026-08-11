def compute(df, params):
    output = df[["data_time", "series_tag"]].copy()
    close = df.groupby("series_tag", sort=False)["close"]
    for raw_window in params["windows"]:
        window = int(raw_window)
        if window < 1:
            raise ValueError("MA window must be at least 1")
        output[f"ma_{window}"] = close.transform(
            lambda values: values.rolling(window, min_periods=1).mean()
        )
    return output
