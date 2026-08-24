# yufeng-modelside

`yufeng-modelside` 是 Edge 邻近的独立 Python 异步推理服务。它不参与当前请求的 Gate 裁决，不连接 Redis、NATS 或 PostgreSQL，也不向 Brain 发送请求头、查询参数或正文。

原生安装：

```sh
python3 -m venv /opt/yufeng/modelside-venv
/opt/yufeng/modelside-venv/bin/pip install '/path/to/components/modelside[tensorflow]'
/opt/yufeng/modelside-venv/bin/yufeng-modelside --help
```

权重目录必须含 `manifest.json`，其 `models` 条目以 `group`、`type`、`version` 精确匹配签名模型档案，并提供相对 `weights` 路径和 `sha256:<hex>` 摘要。同机默认监听 `unix:///run/yufeng/modelside.sock`；跨主机必须使用 HTTPS 与双向 TLS，并由防火墙限制在同一受控防御网络。

容器镜像由 `components/modelside/Dockerfile` 构建；与 Edge 同机部署时使用 `deploy/compose.edge-modelside.yaml`。运行策略只来自每批规范流量携带、且由 Edge 从已验签资产世代导出的模型档案；进程参数不提供告警阈值、复核下限、采样窗或去重规则覆盖项。
