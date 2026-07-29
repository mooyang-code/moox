import unittest
from pathlib import Path
import sys

sys.path.insert(0, str(Path(__file__).parents[1]))

from moox_strategy import ContractError, InstrumentTarget, validate_output


def output(target):
    return {"action": "rebalance", "targets": [target]}


class ValidateOutputTest(unittest.TestCase):
    def test_instrument_target_uses_only_public_identity_and_quantity(self):
        target = InstrumentTarget("BTC-USDT-SPOT", "0.01")
        self.assertEqual(target.quantity, "0.01")

    def test_accepts_empty_rebalance(self):
        value = {"action": "rebalance", "targets": []}
        self.assertIs(validate_output(value), value)

    def test_accepts_instrument_quantity(self):
        value = output({"instrument_id": "BTC-USDT-SPOT", "quantity": "0.01"})
        self.assertIs(validate_output(value), value)

    def test_rejects_duplicate_instrument(self):
        value = output({"instrument_id": "BTC-USDT-SPOT", "quantity": "0.01"})
        value["targets"].append({"instrument_id": "BTC-USDT-SPOT", "quantity": "0.02"})
        with self.assertRaises(ContractError):
            validate_output(value)

    def test_rejects_next_state(self):
        for field in ("state", "next_state"):
            with self.subTest(field=field), self.assertRaises(ContractError):
                validate_output({"action": "hold", "targets": [], field: {}})

    def test_rejects_symbol_and_native_account_fields(self):
        for field in ("symbol", "native_symbol", "account_id", "source", "owner"):
            target = {"instrument_id": "BTC-USDT-SPOT", "quantity": "1", field: "invalid"}
            with self.subTest(field=field), self.assertRaises(ContractError):
                validate_output(output(target))

    def test_rejects_target_quantity_alias(self):
        with self.assertRaises(ContractError):
            validate_output(output({
                "instrument_id": "BTC-USDT-SPOT",
                "target_quantity": "0.01",
            }))

    def test_rejects_oversized_debug_info(self):
        with self.assertRaises(ContractError):
            validate_output({
                "action": "hold",
                "targets": [],
                "debug_info": {"value": "x" * (16 * 1024)},
            })

    def test_rejects_noncanonical_quantities(self):
        for quantity in (
            "+1", ".5", "1.", "01", "1e3", "1/2", "NaN", "Inf",
            " 0.01", "0.01 ", "9" * 257,
        ):
            with self.subTest(quantity=quantity), self.assertRaises(ContractError):
                validate_output(output({
                    "instrument_id": "BTC-USDT-SPOT",
                    "quantity": quantity,
                }))

    def test_rejects_whitespace_target_identity(self):
        for instrument_id in (" ", "\t", " BTC-USDT-SPOT"):
            with self.subTest(instrument_id=instrument_id), self.assertRaises(ContractError):
                validate_output(output({
                    "instrument_id": instrument_id,
                    "quantity": "1",
                }))


if __name__ == "__main__":
    unittest.main()
