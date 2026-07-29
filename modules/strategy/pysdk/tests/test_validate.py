import unittest
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).parents[1]))

from moox_strategy import ContractError, TargetPosition, validate_output


def output(target):
    return {"action": "rebalance", "targets": [target], "next_state": {}}


class ValidateOutputTest(unittest.TestCase):
    def test_target_position_exposes_optional_metadata(self):
        target = TargetPosition(
            "BTC-USDT", "BTCUSDT", "0.01",
            reason="momentum",
            source_time="2026-07-28T00:00:00Z",
            data_revision="view:1",
        )
        self.assertEqual(target.reason, "momentum")

    def test_accepts_final_target_quantity(self):
        value = output({
            "instrument_id": "BTC-USDT",
            "symbol": "BTCUSDT",
            "target_quantity": "0.01",
        })
        self.assertIs(validate_output(value), value)

    def test_rejects_duplicate_symbol(self):
        value = output({
            "instrument_id": "BTC-USDT",
            "symbol": "BTCUSDT",
            "target_quantity": "0.01",
        })
        value["targets"].append({
            "instrument_id": "BTC-USDT-SWAP",
            "symbol": "BTCUSDT",
            "target_quantity": "0.02",
        })
        with self.assertRaises(ContractError):
            validate_output(value)

    def test_rejects_empty_rebalance(self):
        with self.assertRaises(ContractError):
            validate_output({"action": "rebalance", "targets": [], "next_state": {}})

    def test_rejects_legacy_weight_and_missing_quantity(self):
        with self.assertRaises(ContractError):
            validate_output(output({
                "instrument_id": "BTC-USDT",
                "symbol": "BTCUSDT",
                "target_quantity": "0.01",
                "target_weight": "0.5",
            }))

    def test_rejects_noncanonical_quantities(self):
        for quantity in (
            "+1", ".5", "1.", "01", "1e3", "1/2", "NaN", "Inf",
            " 0.01", "0.01 ", "9" * 257,
        ):
            with self.subTest(quantity=quantity), self.assertRaises(ContractError):
                validate_output(output({
                    "instrument_id": "BTC-USDT",
                    "symbol": "BTCUSDT",
                    "target_quantity": quantity,
                }))

    def test_rejects_whitespace_target_identity(self):
        for instrument_id, symbol in ((" ", "BTCUSDT"), ("BTC-USDT", "\t")):
            with self.subTest(instrument_id=instrument_id, symbol=symbol), self.assertRaises(ContractError):
                validate_output(output({
                    "instrument_id": instrument_id,
                    "symbol": symbol,
                    "target_quantity": "1",
                }))


if __name__ == "__main__":
    unittest.main()
