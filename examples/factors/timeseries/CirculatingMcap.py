extra_data_dict = {
    "coin-cap": ["circulating_supply"],
}


def signal(df, n, factor_name):
    df[factor_name] = df["circulating_supply"] * df["close"]
    return df
