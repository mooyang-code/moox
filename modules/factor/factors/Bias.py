def compute(df, params):
    output = df[["data_time", "series_tag"]].copy()
    close = df.groupby("series_tag", sort=False)["close"]
    for window in params["windows"]:
        average = close.transform(
            lambda values: values.rolling(window, min_periods=1).mean()
        )
        output[f"bias_{window}"] = df["close"] / average - 1
    return output