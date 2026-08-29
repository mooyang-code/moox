# stock_cn Provider Probe

- GeneratedAt: `2026-08-29T14:55:22Z`
- Frequency: `1m`
- Subjects: `600000.XSHG`, `000001.XSHE`, `920000.XBSE`

| Provider | Feed | Result | Exchange | Subject | Symbol | HTTP | LatencyMs | ErrorKind |
| --- | --- | --- | --- | --- | --- | ---: | ---: | --- |
| baidu | instrument | NOT_SUPPORTED | ALL | - | - | 0 | 0 | not_supported |

  - pages=0 instruments=0 complete=false exchanges=``
  - note: instrument probe is not implemented

| baidu | kline | FAIL | XSHE | 000001.XSHE | sz000001 | 0 | 0 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "https://finance.pae.baidu.com/api/quotation_minute_ab?code=sz000001&pn=3": dial tcp: lookup finance.pae.baidu.com: no such host

| baidu | kline | FAIL | XSHG | 600000.XSHG | sh600000 | 0 | 0 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "https://finance.pae.baidu.com/api/quotation_minute_ab?code=sh600000&pn=3": dial tcp: lookup finance.pae.baidu.com: no such host

| baidu | kline | FAIL | XBSE | 920000.XBSE | bj920000 | 0 | 0 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "https://finance.pae.baidu.com/api/quotation_minute_ab?code=bj920000&pn=3": dial tcp: lookup finance.pae.baidu.com: no such host

| eastmoney | instrument | NOT_SUPPORTED | ALL | - | - | 0 | 0 | not_supported |

  - pages=0 instruments=0 complete=false exchanges=``
  - note: instrument probe is not implemented

| eastmoney | kline | FAIL | XSHE | 000001.XSHE | sz000001 | 0 | 0 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "https://push2.eastmoney.com/api/qt/stock/trends2/get?fields1=f1%2Cf2%2Cf3%2Cf4%2Cf5%2Cf6%2Cf7%2Cf8%2Cf9%2Cf10%2Cf11%2Cf12%2Cf13&fields2=f51%2Cf52%2Cf53%2Cf54%2Cf55%2Cf56%2Cf57%2Cf58&iscr=0&ndays=5&secid=0.000001": dial tcp: lookup push2.eastmoney.com: no such host

| eastmoney | kline | FAIL | XSHG | 600000.XSHG | sh600000 | 0 | 0 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "https://push2.eastmoney.com/api/qt/stock/trends2/get?fields1=f1%2Cf2%2Cf3%2Cf4%2Cf5%2Cf6%2Cf7%2Cf8%2Cf9%2Cf10%2Cf11%2Cf12%2Cf13&fields2=f51%2Cf52%2Cf53%2Cf54%2Cf55%2Cf56%2Cf57%2Cf58&iscr=0&ndays=5&secid=1.600000": dial tcp: lookup push2.eastmoney.com: no such host

| eastmoney | kline | FAIL | XBSE | 920000.XBSE | bj920000 | 0 | 0 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "https://push2.eastmoney.com/api/qt/stock/trends2/get?fields1=f1%2Cf2%2Cf3%2Cf4%2Cf5%2Cf6%2Cf7%2Cf8%2Cf9%2Cf10%2Cf11%2Cf12%2Cf13&fields2=f51%2Cf52%2Cf53%2Cf54%2Cf55%2Cf56%2Cf57%2Cf58&iscr=0&ndays=5&secid=0.920000": dial tcp: lookup push2.eastmoney.com: no such host

| sina | instrument | NOT_SUPPORTED | ALL | - | - | 0 | 0 | not_supported |

  - pages=0 instruments=0 complete=false exchanges=``
  - note: instrument probe is not implemented

| sina | kline | FAIL | XSHE | 000001.XSHE | sz000001 | 0 | 0 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "https://quotes.sina.cn/cn/api/jsonp_v2.php/var%20moox_kline=/CN_MarketDataService.getKLineData?datalen=3&ma=no&scale=1&symbol=sz000001": dial tcp: lookup quotes.sina.cn: no such host

| sina | kline | FAIL | XSHG | 600000.XSHG | sh600000 | 0 | 11 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "https://quotes.sina.cn/cn/api/jsonp_v2.php/var%20moox_kline=/CN_MarketDataService.getKLineData?datalen=3&ma=no&scale=1&symbol=sh600000": dial tcp: lookup quotes.sina.cn: no such host

| sina | kline | FAIL | XBSE | 920000.XBSE | bj920000 | 0 | 0 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "https://quotes.sina.cn/cn/api/jsonp_v2.php/var%20moox_kline=/CN_MarketDataService.getKLineData?datalen=3&ma=no&scale=1&symbol=bj920000": dial tcp: lookup quotes.sina.cn: no such host

| tencent | instrument | NOT_SUPPORTED | ALL | - | - | 0 | 0 | not_supported |

  - pages=0 instruments=0 complete=false exchanges=``
  - note: instrument probe is not implemented

| tencent | kline | FAIL | XSHE | 000001.XSHE | sz000001 | 0 | 0 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "https://ifzq.gtimg.cn/appstock/app/kline/mkline?_var=m1_today&param=sz000001%2Cm1%2C%2C3": dial tcp: lookup ifzq.gtimg.cn: no such host

| tencent | kline | FAIL | XSHG | 600000.XSHG | sh600000 | 0 | 0 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "https://ifzq.gtimg.cn/appstock/app/kline/mkline?_var=m1_today&param=sh600000%2Cm1%2C%2C3": dial tcp: lookup ifzq.gtimg.cn: no such host

| tencent | kline | FAIL | XBSE | 920000.XBSE | bj920000 | 0 | 0 | timeout |

  - bars=0 latest=`- -> -` earliest=`-` has_ohlcv=false supports_range=false volume_unit=`unknown` amount_unit=`unknown`
  - note: timeout: Get "https://ifzq.gtimg.cn/appstock/app/kline/mkline?_var=m1_today&param=bj920000%2Cm1%2C%2C3": dial tcp: lookup ifzq.gtimg.cn: no such host

