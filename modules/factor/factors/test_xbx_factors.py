import importlib.util
import json
import unittest
from pathlib import Path

import numpy as np
import pandas as pd


FACTORS_DIR = Path(__file__).resolve().parent
FACTOR_NAMES = [
    "Bias",
    "BiasQ",
    "Cci",
    "CirculatingMcap",
    "MinMax",
    "QuoteVolumeMean",
    "QuoteVolumeMeanQ",
    "VolumeMeanQ",
    "ZfAbsMean",
    "ZfMeanQ",
    "ZfStd",
    "ZscoreAbsMeanQ",
]


def load_factor(name):
    path = FACTORS_DIR / f"{name}.py"
    spec = importlib.util.spec_from_file_location(f"moox_test_{name}", path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def sample_frame():
    rows = []
    base = pd.Timestamp("2026-01-01T00:00:00Z")
    for i in range(1, 7):
        for tag, shift in (("binance", 0), ("okx", 2)):
            rows.append({
                "data_time": base + pd.Timedelta(minutes=i),
                "series_tag": tag,
                "open": 10 + i + shift,
                "high": 11 + i + shift,
                "low": 9 + i + shift,
                "close": 10 + i + shift,
                "volume": 100 + i + shift,
                "quote_volume": 1000 + 10 * i + shift,
                "circulating_supply": 1000 + shift,
            })
    return pd.DataFrame(rows)


class XBXFactorTest(unittest.TestCase):
    def test_catalog_covers_all_sources_and_declares_warmup(self):
        catalog = json.loads((FACTORS_DIR / "catalog.json").read_text())
        self.assertEqual(len(catalog), len(FACTOR_NAMES))
        factor_ids = set()
        for entry in catalog:
            with self.subTest(factor=entry["factor_id"]):
                self.assertNotIn(entry["factor_id"], factor_ids)
                factor_ids.add(entry["factor_id"])
                source = FACTORS_DIR / entry["file"]
                self.assertTrue(source.is_file())
                self.assertGreaterEqual(entry["lookback_periods"], 1)
                self.assertEqual(len(entry["outputs"]), 1)

    def test_all_files_expose_moox_compute_and_preserve_identity(self):
        frame = sample_frame()
        for name in FACTOR_NAMES:
            with self.subTest(factor=name):
                module = load_factor(name)
                params = {"window": 3}
                if name not in ("Cci", "CirculatingMcap"):
                    params = {"windows": [3]}
                result = module.compute(frame, params)
                self.assertEqual(len(result), len(frame))
                self.assertEqual(
                    result[["data_time", "series_tag"]].reset_index(drop=True).to_dict("records"),
                    frame[["data_time", "series_tag"]].reset_index(drop=True).to_dict("records"),
                )
                self.assertEqual(list(result.columns[:2]), ["data_time", "series_tag"])
                self.assertEqual(len(result.columns), 3)

    def test_bias_follows_xbx_without_subtracting_one(self):
        frame = sample_frame()
        result = load_factor("Bias").compute(frame, {"windows": [3]})
        expected = frame.groupby("series_tag", sort=False)["close"].transform(
            lambda values: values / values.rolling(3, min_periods=1).mean()
        )
        pd.testing.assert_series_equal(result["bias_3"], expected, check_names=False)

    def test_biasq_and_volume_quantiles_are_group_local(self):
        frame = sample_frame()
        biasq = load_factor("BiasQ").compute(frame, {"windows": [3]})
        close_mean = frame.groupby("series_tag", sort=False)["close"].transform(
            lambda values: values.rolling(3, min_periods=1).mean()
        )
        expected_bias = (frame["close"] / close_mean - 1).groupby(
            frame["series_tag"], sort=False
        ).transform(lambda values: values.rolling(3, min_periods=1).rank(pct=True))
        pd.testing.assert_series_equal(biasq["bias_q_3"], expected_bias, check_names=False)

        volume = load_factor("VolumeMeanQ").compute(frame, {"windows": [3]})
        mean = frame.groupby("series_tag", sort=False)["volume"].transform(
            lambda values: values.rolling(3, min_periods=1).mean()
        )
        expected_volume = mean.groupby(frame["series_tag"], sort=False).transform(
            lambda values: values.rolling(3, min_periods=1).rank(pct=True)
        )
        pd.testing.assert_series_equal(volume["volume_mean_q_3"], expected_volume, check_names=False)

    def test_cci_and_mcap_match_reference_equations(self):
        frame = sample_frame()
        cci = load_factor("Cci").compute(frame, {"window": 3})
        typical = (frame["high"] + frame["low"] + frame["close"]) / 3
        grouped = typical.groupby(frame["series_tag"], sort=False)
        mean = grouped.transform(lambda values: values.rolling(3, min_periods=1).mean())
        deviation = (typical - mean).abs().groupby(frame["series_tag"], sort=False).transform(
            lambda values: values.rolling(3, min_periods=1).mean()
        )
        pd.testing.assert_series_equal(
            cci["cci"], (typical - mean) / (deviation * 0.015), check_names=False
        )

        mcap = load_factor("CirculatingMcap").compute(frame, {})
        pd.testing.assert_series_equal(
            mcap["circulating_mcap"], frame["circulating_supply"] * frame["close"], check_names=False
        )

    def test_zf_abs_mean_uses_typical_price_direction(self):
        frame = sample_frame()
        # Make close-only direction disagree with typical-price direction for
        # one venue row; the reference uses (close + high + low) / 3.
        frame.loc[(frame["series_tag"] == "binance") & (frame["data_time"] == frame["data_time"].min()), "close"] = 5
        result = load_factor("ZfAbsMean").compute(frame, {"windows": [2]})
        typical = (frame["close"] + frame["high"] + frame["low"]) / 3
        change = typical.groupby(frame["series_tag"], sort=False).pct_change()
        amplitude = (frame["high"] - frame["low"]) / frame["open"]
        expected = pd.Series(0.0, index=frame.index)
        expected.loc[change > 0] = amplitude.loc[change > 0]
        means = expected.groupby(frame["series_tag"], sort=False).transform(
            lambda values: values.rolling(2, min_periods=1).mean()
        )
        expected = means.groupby(frame["series_tag"], sort=False).transform(
            lambda values: values.rolling(2, min_periods=1).rank(ascending=True, pct=True)
        )
        pd.testing.assert_series_equal(result["zf_abs_mean_2"], expected, check_names=False)

    def test_remaining_factors_match_xbx_golden_values(self):
        # Fixed one-series fixture with hand-checked XBX outputs. Keeping the
        # expected vectors literal makes this a parity test rather than a
        # second implementation of each rolling equation.
        frame = pd.DataFrame([
            {"data_time": "2026-01-01T00:01:00Z", "series_tag": "x", "open": 10, "high": 12, "low": 9, "close": 11, "quote_volume": 100},
            {"data_time": "2026-01-01T00:02:00Z", "series_tag": "x", "open": 11, "high": 14, "low": 10, "close": 13, "quote_volume": 200},
            {"data_time": "2026-01-01T00:03:00Z", "series_tag": "x", "open": 12, "high": 13, "low": 11, "close": 12, "quote_volume": 150},
            {"data_time": "2026-01-01T00:04:00Z", "series_tag": "x", "open": 13, "high": 17, "low": 12, "close": 16, "quote_volume": 300},
        ])
        expected = {
            "MinMax": [np.nan, 0.5, 0.3, 0.5],
            "QuoteVolumeMean": [100.0, 150.0, 150.0, 216.66666666666666],
            "QuoteVolumeMeanQ": [1.0, 1.0, 0.8333333333333334, 1.0],
            "ZfMeanQ": [1.0, 1.0, 0.3333333333333333, 0.6666666666666666],
            "ZfStd": [np.nan, 0.46926177296925425, 0.3510489768457887, 0.312402871128388],
            "ZscoreAbsMeanQ": [np.nan, 1.0, 0.5, 0.6666666666666666],
        }
        for name, values in expected.items():
            with self.subTest(factor=name):
                result = load_factor(name).compute(frame, {"windows": [3]})
                np.testing.assert_allclose(result.iloc[:, 2].to_numpy(), values, equal_nan=True, rtol=1e-12, atol=1e-12)


if __name__ == "__main__":
    unittest.main()
