# ModelSide 原生服务

`yufeng-modelside` 是可独立安装的 Python 服务，不进入任何 Go 平台二进制。它只接收 Edge 已规范化的 HTTP 请求副本并异步推理，不持 Gate 权限，也不连接 PostgreSQL、Redis 或 NATS 消息服务器。

安装示例：

```sh
sudo useradd --system --home /var/lib/yufeng --shell /usr/sbin/nologin yufeng
sudo python3 -m venv /opt/yufeng/modelside
sudo /opt/yufeng/modelside/bin/pip install './wheels/yufeng_modelside-0.2.0-py3-none-any.whl[tensorflow]'
sudo install -m 0644 deploy/modelside/yufeng-modelside.service /etc/systemd/system/
sudo install -m 0640 -o root -g yufeng deploy/modelside/modelside.env.example /etc/yufeng/modelside.env
sudo systemctl daemon-reload
sudo systemctl enable --now yufeng-modelside
```

权重目录必须含 `manifest.json` 和与签名模型档案精确匹配、摘要正确的权重。相互传输层安全协议私钥与结果上报令牌只放在 `/etc/yufeng` 的受限文件中。跨主机监听时，把 `YUFENG_MODELSIDE_LISTEN` 改为 HTTPS，并在服务命令中追加 `--listen-ca`、`--listen-cert`、`--listen-key`；防火墙必须限制在同一受控防御网络。
