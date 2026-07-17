import numpy as np


def signal(df, n, factor_name):
    target = str(n).replace("-", "")
    symbol = df["symbol"].str.replace("-", "", regex=False)
    df[factor_name] = np.where(symbol == target, 1, np.nan)
    return df
