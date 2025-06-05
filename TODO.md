
- 数据库初始化工具：
  1. 元数据表初始化：项目定义表、字段定义表等。使用该工具可以从yaml中读取用户配置，一键建表并写入。【对于当前已有元数据的，弹出提示信息，让用户决定是否覆盖】
  2. 数据表：对于有Schama的存储组件，可以使用该工具一键建数据表（读取上一步用户的元数据配置），或DDL。（例如使用DuckDB的）


- 初始化xData配置：
  1. ./moox db --meta-schema=schema.sql
  2. ./moox db --insert-data=metadata.yaml

- 测试读写操作：
  1. ./moox storage --interface=set --project-id=1 --dataset-id=101 --object-id=BTCUSDT --freq=1H
  2. ./moox storage --interface=get --project-id=1 --dataset-id=101 --object-id=BTCUSDT --freq=1H
  3. ./moox storage --interface=search --project-id=1 --dataset-id=101 --object-id=BTCUSDT --freq=1H

