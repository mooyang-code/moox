import numpy as np
import pandas as pd


def signal(df, n, factor_name):
    candle_time = pd.to_datetime(pd.Series(df["candle_begin_time"], index=df.index))
    eligible = (df["symbol_swap"] != "") & (df["symbol_spot"] != "")
    if not eligible.any():
        df[factor_name] = np.nan
        return df

    first_index = eligible[eligible].index[0]
    first_time = candle_time.loc[first_index]
    hours = (candle_time - first_time).dt.total_seconds() / 3600
    df[factor_name] = np.where(eligible, hours, np.nan)
    return df
