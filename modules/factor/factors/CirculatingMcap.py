"""Circulating market capitalisation from ordinary input columns."""


def compute(df, params):
    output = df[["data_time", "series_tag"]].copy()
    output[params.get("output", "circulating_mcap")] = df["circulating_supply"] * df["close"]
    return output