# Stock US Provider Qualification

## Decision

US-1 remains **no-go**. `stock_us` must stay `runtime_enabled: false` until a business-authorized account, target-SCF network probe, quota probe, adjustment comparison and redistribution review are recorded in `provider-validation.yaml`.

The implementation candidate order is:

1. `alpaca_sip` as primary, but only with an Algo Trader Plus or separately approved business entitlement that exposes SIP historical data.
2. `massive_sip` as fallback, but only with a plan and license approved for the MooX deployment and its users.

Free IEX-only Alpaca data is not an acceptable fallback for a consolidated `stock_us` dataset. It represents one exchange rather than the complete US market. Massive personal plans are not assumed to permit server-side redistribution.

## Candidate Matrix

| Requirement | Alpaca Market Data | Massive Stocks | Qualification result |
| --- | --- | --- | --- |
| Equity and ETF universe | US stocks and ETFs | Ticker reference across US markets | Documentation supports; live pagination untested |
| Consolidated scope | `feed=sip`; Basic defaults to IEX and delayed SIP constraints | SIP-derived coverage across major exchanges, FINRA and dark pools | Both require paid/authorized entitlement |
| Daily/minute history | Historical bars since 2016 according to plan documentation | Custom aggregate bars with minute/day timespans | Supported in documentation; exact retention by plan must be probed |
| Adjustment | Must probe stock bars and corporate-action samples; do not assume default | `adjusted=false` explicitly returns unadjusted bars | Massive contract is explicit; Alpaca still pending sample evidence |
| Pagination | `next_page_token` must be exercised | `next_url`, max base aggregate limit documented as 50,000 | Pending live fixtures |
| Rate limits | Basic 200/min; Algo Trader Plus 10,000/min in current plan table | Plan-specific; response limits and account quota must be captured | Pending account-specific headers |
| Minute anchor | SIP timestamps; normalize to America/New_York sessions | Aggregate documentation states Eastern Time and extended sessions | DST boundary probes still required |
| Index support | Stock feed does not establish broad index coverage | Separate indices product, not proven by Stocks entitlement | **Not qualified** |
| License | Account and market-data agreement required | Market-data terms restrict redistribution unless authorized | Legal/business review required |
| SCF reachability | `data.alpaca.markets:443` | `api.massive.com:443` | Not probed from target SCF |

## Authoritative References

- Alpaca market-data plans and feed scope: <https://docs.alpaca.markets/docs/about-market-data-api>
- Alpaca latest bars and `sip`/`iex`/`delayed_sip` feeds: <https://docs.alpaca.markets/reference/stocklatestbars-1>
- Massive Stocks REST scope: <https://massive.com/docs/rest/stocks>
- Massive custom bars and `adjusted=false`: <https://massive.com/docs/rest/stocks/aggregates/custom-bars>
- Massive unadjusted flat-file semantics: <https://massive.com/docs/flat-files/stocks/overview>
- Massive market-data terms: <https://massive.com/terms/market_data_terms.pdf>

## Required Live Evidence

The gate owner must run probes from the target Tencent SCF environment and retain only summaries/fingerprints:

1. Fetch all active equity and ETF instruments with pagination and compare counts on two consecutive runs.
2. Fetch AAPL and SPY daily plus one-minute bars using SIP scope.
3. Fetch samples across the March and November America/New_York DST transitions.
4. Compare `adjustment=none` around at least one split and one dividend.
5. Record response quota headers, 429 behavior, maximum symbols/points, timeout and pagination tokens.
6. Confirm whether index bars require a separate licensed product. Do not enable the index capability without it.
7. Record license approval identifier outside Git; record only `approved=true/false` and the permitted application scope here.

No token, secret, response body containing account data, or agreement document belongs in this repository.
