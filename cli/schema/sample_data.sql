-- ============ t_collector_task_instances 示例数据 ============

-- 成功的数据采集任务实例
INSERT INTO t_collector_task_instances (
    c_task_id, c_rule_id, c_node_id, c_task_params, c_status, c_start_time, c_end_time, c_result
) VALUES 
(
    'task_kline_btc_usdt_001', 
    'rule_kline_binance_001', 
    'scf-collector-01',
    '{"symbol": "BTCUSDT", "interval": "1m", "limit": 100}',
    2,
    '2025-11-08 10:00:00',
    '2025-11-08 10:00:15',
    '{"data_count": 100, "success_rate": 100, "avg_response_time": 0.15}'
),
(
    'task_ticker_eth_usdt_001', 
    'rule_ticker_okx_001', 
    'scf-collector-02',
    '{"symbol": "ETHUSDT", "fields": ["last", "bid", "ask", "volume"]}',
    2,
    '2025-11-08 10:01:00',
    '2025-11-08 10:01:02',
    '{"last_price": "3245.50", "bid": "3245.45", "ask": "3245.55", "volume": "1256.78"}'
);

-- 执行中的任务实例
INSERT INTO t_collector_task_instances (
    c_task_id, c_rule_id, c_node_id, c_task_params, c_status, c_start_time, c_result
) VALUES 
(
    'task_orderbook_btc_usdt_001', 
    'rule_orderbook_binance_001', 
    'scf-collector-01',
    '{"symbol": "BTCUSDT", "depth": 20}',
    1,
    '2025-11-08 10:05:00',
    '{"progress": 75, "processed_count": 75}'
);

-- 待执行的任务实例
INSERT INTO t_collector_task_instances (
    c_task_id, c_rule_id, c_node_id, c_task_params, c_status
) VALUES 
(
    'task_trade_btc_usdt_001', 
    'rule_trade_binance_001', 
    'scf-collector-03',
    '{"symbol": "BTCUSDT", "limit": 50}',
    0
),
(
    'task_news_market_001', 
    'rule_news_cryptonews_001', 
    'scf-collector-02',
    '{"category": "bitcoin", "language": "en", "limit": 20}',
    0
);

-- 部分失败的任务实例
INSERT INTO t_collector_task_instances (
    c_task_id, c_rule_id, c_node_id, c_task_params, c_status, c_start_time, c_end_time, c_result
) VALUES 
(
    'task_kline_mixed_symbols_001', 
    'rule_kline_multi_binance_001', 
    'scf-collector-01',
    '{"symbols": ["BTCUSDT", "ETHUSDT", "ADAUSDT"], "interval": "5m", "limit": 100}',
    3,
    '2025-11-08 09:55:00',
    '2025-11-08 10:00:30',
    '{"total_symbols": 3, "success_symbols": 2, "failed_symbols": ["ADAUSDT"], "success_rate": 66.67}'
);

-- 失败的任务实例
INSERT INTO t_collector_task_instances (
    c_task_id, c_rule_id, c_node_id, c_task_params, c_status, c_start_time, c_end_time, c_result
) VALUES 
(
    'task_kline_api_timeout_001', 
    'rule_kline_binance_002', 
    'scf-collector-02',
    '{"symbol": "DOGEUSDT", "interval": "1m", "limit": 200}',
    4,
    '2025-11-08 10:03:00',
    '2025-11-08 10:03:35',
    '{"error": "API timeout", "timeout_duration": 35, "retry_count": 3}'
);

-- 不同节点的数据采集实例
INSERT INTO t_collector_task_instances (
    c_task_id, c_rule_id, c_node_id, c_task_params, c_status, c_start_time, c_end_time, c_result
) VALUES 
(
    'task_ticker_multi_exchanges_001', 
    'rule_ticker_multi_001', 
    'scf-collector-04',
    '{"exchanges": ["binance", "okx", "huobi"], "symbol": "BTCUSDT"}',
    2,
    '2025-11-08 10:02:00',
    '2025-11-08 10:02:08',
    '{"exchanges_data": {"binance": {"price": "67500.25"}, "okx": {"price": "67498.50"}, "huobi": {"price": "67501.00}}}'
);

-- 历史数据采集任务实例
INSERT INTO t_collector_task_instances (
    c_task_id, c_rule_id, c_node_id, c_task_params, c_status, c_start_time, c_end_time, c_result
) VALUES 
(
    'task_kline_historical_001', 
    'rule_kline_historical_binance_001', 
    'scf-collector-01',
    '{"symbol": "BTCUSDT", "interval": "1h", "start_time": "2025-11-01 00:00:00", "end_time": "2025-11-07 23:59:59"}',
    2,
    '2025-11-08 08:00:00',
    '2025-11-08 09:45:30',
    '{"total_candles": 168, "date_range": "2025-11-01 to 2025-11-07", "file_size": "2.3MB"}'
);

-- 不同数据类型的任务实例
INSERT INTO t_collector_task_instances (
    c_task_id, c_rule_id, c_node_id, c_task_params, c_status, c_start_time, c_end_time, c_result
) VALUES 
(
    'task_depth_btc_usdt_001', 
    'rule_depth_binance_001', 
    'scf-collector-02',
    '{"symbol": "BTCUSDT", "depth": 10, "type": "step1"}',
    2,
    '2025-11-08 10:04:00',
    '2025-11-08 10:04:01',
    '{"bids": [["67500.50", "0.125"], ["67500.25", "0.340"]], "asks": [["67500.75", "0.089"], ["67501.00", "0.156"]]}'
),
(
    'task_funding_rate_001', 
    'rule_funding_okx_001', 
    'scf-collector-03',
    '{"instrument": "BTC-USDT-SWAP", "funding_time": "next"}',
    2,
    '2025-11-08 10:06:00',
    '2025-11-08 10:06:01',
    '{"funding_rate": "0.0001", "funding_time": "2025-11-08 16:00:00", "predicted_rate": "0.0002"}'
);