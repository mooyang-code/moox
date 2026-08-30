# stock_cn Provider Probe

Release-route note: the current source route keeps Sina as the only active
Instrument provider until EastMoney produces a complete stable snapshot. Sina,
Tencent, and EastMoney remain the three active K-line candidates; Baidu K-line
is shadow and Baidu Instrument is disabled. This reflects the probe evidence
below and is deliberately fail-closed, not a claim that all four providers are
production healthy.

- GeneratedAt: `2026-08-30T02:38:19Z`
- Frequency: `1m`
- Subjects: `600000.XSHG`, `000001.XSHE`, `920000.XBSE`

| Provider | Feed | Result | Exchange | Subject | Symbol | HTTP | LatencyMs | ErrorKind |
| --- | --- | --- | --- | --- | --- | ---: | ---: | --- |
| baidu | instrument | FAIL | ALL | - | - | 200 | 23 | protocol |

  - pages=0 instruments=0 complete=false exchanges=``
  - note: protocol error: unexpected end of JSON input

| baidu | kline | FAIL | XSHE | 000001.XSHE | sz000001 | 200 | 23 | protocol |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: protocol error: unexpected end of JSON input

| baidu | kline | FAIL | XSHG | 600000.XSHG | sh600000 | 200 | 203 | protocol |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: protocol error: unexpected end of JSON input

| baidu | kline | FAIL | XBSE | 920000.XBSE | bj920000 | 200 | 26 | protocol |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: protocol error: unexpected end of JSON input

| eastmoney | instrument | FAIL | ALL | - | - | 0 | 212 | timeout |

  - pages=0 instruments=0 complete=false exchanges=``
  - note: timeout: Get "https://push2.eastmoney.com/api/qt/clist/get?fid=f12&fields=f12%2Cf13%2Cf14%2Cf115%2Cf152%2Cf103%2Cf128%2Cf129&fs=m%3A0+t%3A6%2Cm%3A0+t%3A80%2Cm%3A1+t%3A2&pn=1&pz=500": EOF

| eastmoney | kline | FAIL | XSHE | 000001.XSHE | sz000001 | 0 | 216 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "https://push2.eastmoney.com/api/qt/stock/kline/get?secid=0.000001&klt=1&fqt=0&beg=0&end=20260830&lmt=3&fields1=f1,f2,f3,f4,f5,f6,f7,f8,f9,f10,f11,f12,f13&fields2=f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61": EOF

| eastmoney | kline | FAIL | XSHG | 600000.XSHG | sh600000 | 0 | 441 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "https://push2.eastmoney.com/api/qt/stock/kline/get?secid=1.600000&klt=1&fqt=0&beg=0&end=20260830&lmt=3&fields1=f1,f2,f3,f4,f5,f6,f7,f8,f9,f10,f11,f12,f13&fields2=f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61": EOF

| eastmoney | kline | FAIL | XBSE | 920000.XBSE | bj920000 | 0 | 221 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "https://push2.eastmoney.com/api/qt/stock/kline/get?secid=0.920000&klt=1&fqt=0&beg=0&end=20260830&lmt=3&fields1=f1,f2,f3,f4,f5,f6,f7,f8,f9,f10,f11,f12,f13&fields2=f51,f52,f53,f54,f55,f56,f57,f58,f59,f60,f61": EOF

| sina | instrument | PASS | ALL | - | - | 200 | 24822 | none |

  - pages=56 instruments=5550 complete=true exchanges=`XBSE`, `XSHE`, `XSHG`

| sina | kline | PASS | XSHE | 000001.XSHE | sz000001 | 200 | 1424 | none |

  - bars=3 latest=`2026-08-28T06:59:00Z -> 2026-08-28T07:00:00Z` earliest=`2026-08-28T06:55:00Z` has_ohlcv=true supports_range=true volume_unit=`shares` amount_unit=`cny` history=PASS history_bars=240 requested_start=`2026-08-29T02:38:23Z` effective_start=`2026-08-28T06:59:00Z` rate_requests=15 rate_failed=0 rate_429=0 rate_p95_ms=63 observed_rps=65.53

| sina | kline | FAIL | XSHG | 600000.XSHG | sh600000 | 0 | 2003 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: sensitive transport details redacted

| sina | kline | PASS | XBSE | 920000.XBSE | bj920000 | 200 | 60 | none |

  - bars=3 latest=`2026-08-28T06:59:00Z -> 2026-08-28T07:00:00Z` earliest=`2026-08-28T06:56:00Z` has_ohlcv=true supports_range=false volume_unit=`shares` amount_unit=`cny`

| tencent | instrument | NOT_SUPPORTED | ALL | - | - | 0 | 0 | not_supported |

  - pages=0 instruments=0 complete=false exchanges=``
  - note: instrument probe is not implemented

| tencent | kline | PASS | XSHE | 000001.XSHE | sz000001 | 200 | 39 | none |

  - bars=3 latest=`2026-08-28T06:59:00Z -> 2026-08-28T07:00:00Z` earliest=`2026-08-28T06:57:00Z` has_ohlcv=true supports_range=false volume_unit=`shares` amount_unit=`not_available`

| tencent | kline | PASS | XSHG | 600000.XSHG | sh600000 | 200 | 221 | none |

  - bars=3 latest=`2026-08-28T06:59:00Z -> 2026-08-28T07:00:00Z` earliest=`2026-08-28T06:57:00Z` has_ohlcv=true supports_range=true volume_unit=`shares` amount_unit=`not_available` history=PASS history_bars=240 requested_start=`2026-08-29T02:38:23Z` effective_start=`2026-08-28T06:59:00Z` rate_requests=15 rate_failed=0 rate_429=0 rate_p95_ms=45 observed_rps=89.36

| tencent | kline | NOT_SUPPORTED | XBSE | 920000.XBSE | bj920000 | 0 | 0 | unsupported_exchange |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: provider spec does not include this exchange
