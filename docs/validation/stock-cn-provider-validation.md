# stock_cn Provider Probe

- GeneratedAt: `2026-08-29T15:32:45Z`
- Frequency: `1m`
- Subjects: `600000.XSHG`, `000001.XSHE`, `920000.XBSE`

| Provider | Feed | Result | Exchange | Subject | Symbol | HTTP | LatencyMs | ErrorKind |
| --- | --- | --- | --- | --- | --- | ---: | ---: | --- |
| baidu | instrument | FAIL | ALL | - | - | 200 | 28 | protocol |

  - pages=0 instruments=0 complete=false exchanges=``
  - note: protocol error: unexpected end of JSON input

| baidu | kline | FAIL | XSHE | 000001.XSHE | sz000001 | 200 | 49 | protocol |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: protocol error: unexpected end of JSON input

| baidu | kline | FAIL | XSHG | 600000.XSHG | sh600000 | 200 | 126 | protocol |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: protocol error: unexpected end of JSON input

| baidu | kline | FAIL | XBSE | 920000.XBSE | bj920000 | 200 | 23 | protocol |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: protocol error: unexpected end of JSON input

| eastmoney | instrument | FAIL | ALL | - | - | 0 | 142 | timeout |

  - pages=0 instruments=0 complete=false exchanges=``
  - note: timeout: Get "http://push2.eastmoney.com/api/qt/clist/get?fid=f12&fields=f12%2Cf13%2Cf14%2Cf115%2Cf152%2Cf103%2Cf128%2Cf129&fs=m%3A0+t%3A6%2Cm%3A0+t%3A80%2Cm%3A1+t%3A2&pn=1&pz=500": EOF

| eastmoney | kline | FAIL | XSHE | 000001.XSHE | sz000001 | 0 | 191 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "http://push2.eastmoney.com/api/qt/stock/trends2/get?secid=0.000001&ndays=5&iscr=0&fields1=f1,f2,f3,f4,f5,f6,f7,f8,f9,f10,f11,f12,f13&fields2=f51,f52,f53,f54,f55,f56,f57,f58": EOF

| eastmoney | kline | FAIL | XSHG | 600000.XSHG | sh600000 | 0 | 183 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "http://push2.eastmoney.com/api/qt/stock/trends2/get?secid=1.600000&ndays=5&iscr=0&fields1=f1,f2,f3,f4,f5,f6,f7,f8,f9,f10,f11,f12,f13&fields2=f51,f52,f53,f54,f55,f56,f57,f58": EOF

| eastmoney | kline | FAIL | XBSE | 920000.XBSE | bj920000 | 0 | 131 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "http://push2.eastmoney.com/api/qt/stock/trends2/get?secid=0.920000&ndays=5&iscr=0&fields1=f1,f2,f3,f4,f5,f6,f7,f8,f9,f10,f11,f12,f13&fields2=f51,f52,f53,f54,f55,f56,f57,f58": EOF

| sina | instrument | PASS | ALL | - | - | 200 | 30115 | none |

  - pages=56 instruments=5550 complete=true exchanges=`XBSE`, `XSHE`, `XSHG`

| sina | kline | PASS | XSHE | 000001.XSHE | sz000001 | 200 | 82 | none |

  - bars=3 latest=`2026-08-28T07:00:00Z -> 2026-08-28T07:01:00Z` earliest=`2026-08-28T06:56:00Z` has_ohlcv=true supports_range=false volume_unit=`shares` amount_unit=`cny`

| sina | kline | PASS | XSHG | 600000.XSHG | sh600000 | 200 | 3558 | none |

  - bars=3 latest=`2026-08-28T07:00:00Z -> 2026-08-28T07:01:00Z` earliest=`2026-08-28T06:56:00Z` has_ohlcv=true supports_range=false volume_unit=`shares` amount_unit=`cny`

| sina | kline | PASS | XBSE | 920000.XBSE | bj920000 | 200 | 92 | none |

  - bars=3 latest=`2026-08-28T07:00:00Z -> 2026-08-28T07:01:00Z` earliest=`2026-08-28T06:57:00Z` has_ohlcv=true supports_range=false volume_unit=`shares` amount_unit=`cny`

| tencent | instrument | NOT_SUPPORTED | ALL | - | - | 0 | 0 | not_supported |

  - pages=0 instruments=0 complete=false exchanges=``
  - note: instrument probe is not implemented

| tencent | kline | PASS | XSHE | 000001.XSHE | sz000001 | 200 | 60 | none |

  - bars=3 latest=`2026-08-28T06:59:00Z -> 2026-08-28T07:00:00Z` earliest=`2026-08-28T06:57:00Z` has_ohlcv=true supports_range=false volume_unit=`shares` amount_unit=`not_available`

| tencent | kline | PASS | XSHG | 600000.XSHG | sh600000 | 200 | 128 | none |

  - bars=3 latest=`2026-08-28T06:59:00Z -> 2026-08-28T07:00:00Z` earliest=`2026-08-28T06:57:00Z` has_ohlcv=true supports_range=false volume_unit=`shares` amount_unit=`not_available`

| tencent | kline | FAIL | XBSE | 920000.XBSE | bj920000 | 200 | 69 | no_closed_bar |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: no closed bar

