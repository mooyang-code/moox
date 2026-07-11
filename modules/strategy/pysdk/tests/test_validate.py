import pytest
from moox_strategy import ContractError, validate_output

def test_validate_output_rejects_duplicate_targets():
    with pytest.raises(ContractError):
        validate_output({"action":"rebalance","targets":[{"instrument_id":"BTC","target_weight":"0.5"},{"instrument_id":"BTC","target_weight":"0.5"}],"next_state":{}})
