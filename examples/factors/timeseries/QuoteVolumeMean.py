def signal(df, n, factor_name):
    n = int(n)
    df[factor_name] = df["quote_volume"].rolling(n, min_periods=1).mean()
    return df
