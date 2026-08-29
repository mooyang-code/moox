# stock_cn Provider Probe

- GeneratedAt: `2026-08-29T14:57:13Z`
- Frequency: `1m`
- Subjects: `600000.XSHG`, `000001.XSHE`, `920000.XBSE`

| Provider | Feed | Result | Exchange | Subject | Symbol | HTTP | LatencyMs | ErrorKind |
| --- | --- | --- | --- | --- | --- | ---: | ---: | --- |
| baidu | kline | FAIL | XSHE | 000001.XSHE | sz000001 | 200 | 27 | protocol |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: protocol error: unexpected end of JSON input

| baidu | kline | FAIL | XSHG | 600000.XSHG | sh600000 | 200 | 84 | protocol |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: protocol error: unexpected end of JSON input

| baidu | kline | FAIL | XBSE | 920000.XBSE | bj920000 | 200 | 56 | protocol |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: protocol error: unexpected end of JSON input

| eastmoney | kline | FAIL | XSHE | 000001.XSHE | sz000001 | 0 | 66 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "http://push2.eastmoney.com/api/qt/stock/trends2/get?fields1=f1%2Cf2%2Cf3%2Cf4%2Cf5%2Cf6%2Cf7%2Cf8%2Cf9%2Cf10%2Cf11%2Cf12%2Cf13&fields2=f51%2Cf52%2Cf53%2Cf54%2Cf55%2Cf56%2Cf57%2Cf58&iscr=0&ndays=5&secid=0.000001": EOF

| eastmoney | kline | FAIL | XSHG | 600000.XSHG | sh600000 | 0 | 75 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "http://push2.eastmoney.com/api/qt/stock/trends2/get?fields1=f1%2Cf2%2Cf3%2Cf4%2Cf5%2Cf6%2Cf7%2Cf8%2Cf9%2Cf10%2Cf11%2Cf12%2Cf13&fields2=f51%2Cf52%2Cf53%2Cf54%2Cf55%2Cf56%2Cf57%2Cf58&iscr=0&ndays=5&secid=1.600000": EOF

| eastmoney | kline | FAIL | XBSE | 920000.XBSE | bj920000 | 0 | 79 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "http://push2.eastmoney.com/api/qt/stock/trends2/get?fields1=f1%2Cf2%2Cf3%2Cf4%2Cf5%2Cf6%2Cf7%2Cf8%2Cf9%2Cf10%2Cf11%2Cf12%2Cf13&fields2=f51%2Cf52%2Cf53%2Cf54%2Cf55%2Cf56%2Cf57%2Cf58&iscr=0&ndays=5&secid=0.920000": EOF

| sina | kline | PASS | XSHE | 000001.XSHE | sz000001 | 200 | 58 | none |

  - bars=3 latest=`2026-08-28T07:00:00Z -> 2026-08-28T07:01:00Z` earliest=`2026-08-28T06:56:00Z` has_ohlcv=true supports_range=false volume_unit=`shares` amount_unit=`cny`

| sina | kline | PASS | XSHG | 600000.XSHG | sh600000 | 200 | 258 | none |

  - bars=3 latest=`2026-08-28T07:00:00Z -> 2026-08-28T07:01:00Z` earliest=`2026-08-28T06:56:00Z` has_ohlcv=true supports_range=false volume_unit=`shares` amount_unit=`cny`

| sina | kline | PASS | XBSE | 920000.XBSE | bj920000 | 200 | 54 | none |

  - bars=3 latest=`2026-08-28T07:00:00Z -> 2026-08-28T07:01:00Z` earliest=`2026-08-28T06:57:00Z` has_ohlcv=true supports_range=false volume_unit=`shares` amount_unit=`cny`

| tencent | kline | PASS | XSHE | 000001.XSHE | sz000001 | 200 | 36 | none |

  - bars=3 latest=`2026-08-28T06:59:00Z -> 2026-08-28T07:00:00Z` earliest=`2026-08-28T06:57:00Z` has_ohlcv=true supports_range=false volume_unit=`shares` amount_unit=`not_available`

| tencent | kline | PASS | XSHG | 600000.XSHG | sh600000 | 200 | 103 | none |

  - bars=3 latest=`2026-08-28T06:59:00Z -> 2026-08-28T07:00:00Z` earliest=`2026-08-28T06:57:00Z` has_ohlcv=true supports_range=false volume_unit=`shares` amount_unit=`not_available`

| tencent | kline | FAIL | XBSE | 920000.XBSE | bj920000 | 200 | 33 | no_closed_bar |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: no closed bar

