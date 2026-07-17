def signal(df, n, factor_name):
    n = int(n)
    spot = df["is_spot"] == 1
    spot_frame = df.loc[spot]
    rank = spot_frame.groupby("candle_begin_time")[f"QuoteVolumeMean_{n}"].rank(
        ascending=True,
        method="min",
    )
    df.loc[spot, factor_name] = rank.groupby(spot_frame["symbol"]).transform(
        lambda values: values.pct_change(periods=n)
    )
    return df


def get_factor_list(n):
    return [("QuoteVolumeMean", int(n))]
