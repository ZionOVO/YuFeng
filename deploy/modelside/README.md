# ModelSide 人工安装与权重管理

`yufeng-modelside` 是独立 Python 服务，只接收 Edge 已规范化的超文本传输协议请求副本并异步推理。它不持 Gate 权限，不连接 PostgreSQL、Redis 或 NATS 消息服务器，也不会把原始头、查询参数或正文上报 Brain。

## 1. 身份、网络与所需文件

资产详情中的 Edge 人工接入配置会确定性给出 ModelSide 身份。启动参数 `--modelside-id` 必须与该值一致。技术人员还需准备：

- 发布清单校验通过的 Python wheel 与本目录的 systemd 物料；
- ModelSide 专用结果上报令牌文件；
- Brain 信任根、ModelSide 到 Brain 的客户端证书和私钥；
- 不可变权重目录及 `manifest.json`；
- 跨主机接收 Edge 流量时，另备 ModelSide 服务端证书、私钥、Edge 客户端证书信任根和精确防火墙规则。

Brain 不会向 ModelSide 反向拨号。ModelSide 主动上报结果，Brain 不可达时使用现有有界结果队列退避重试；队列满时按运行语义丢弃并计数，不向 Edge 或当前业务请求施加背压。因此 ModelSide 位于网络地址转换设备之后时，不需要为 Brain 建立入站端口映射。

Edge 到分机 ModelSide 仍需可达。由技术人员提供受控专网、私有覆盖网络或运维隧道，并使用双向传输层安全协议；不得因为 ModelSide 位于网络地址转换设备之后而退化为公网明文或跳过客户端证书。

## 2. 权重清单

权重根目录必须包含 `manifest.json`。每项 `group`、`type`、`version` 要与 Brain 签名模型档案精确匹配；`weights` 必须是根目录内的相对路径；`sha256` 是权重文件的安全哈希算法 256 位摘要。例如 GPVM 测试权重 `http-threat/PVM/gpvm-e9eceef3` 可写为：

```json
{
  "models": [
    {
      "group": "http-threat",
      "type": "PVM",
      "version": "gpvm-e9eceef3",
      "weights": "http-threat/PVM/gpvm-e9eceef3.weights.h5",
      "sha256": "sha256:<64 个小写十六进制字符>"
    }
  ]
}
```

上线前在目标主机复核：

```sh
sha256sum /opt/yufeng/models/http-threat/PVM/gpvm-e9eceef3.weights.h5
python3 -m json.tool /opt/yufeng/models/manifest.json >/dev/null
```

ModelSide 在首次使用某个签名坐标时再次计算摘要；文件缺失、坐标不匹配或摘要不一致都会失败关闭，不能回退到别的权重。

## 3. 原生 Linux 服务

```sh
sudo useradd --system --home /var/lib/yufeng --shell /usr/sbin/nologin yufeng
sudo python3 -m venv /opt/yufeng/modelside
sudo /opt/yufeng/modelside/bin/pip install './wheels/yufeng_modelside-0.1.2-py3-none-any.whl[tensorflow]'
sudo install -m 0644 deploy/modelside/yufeng-modelside.service /etc/systemd/system/
sudo install -m 0640 -o root -g yufeng deploy/modelside/modelside.env.example /etc/yufeng/modelside.env
sudo systemctl daemon-reload
sudo systemctl enable --now yufeng-modelside
sudo systemctl status --no-pager yufeng-modelside
```

同机 Edge 与 ModelSide 使用 `unix:///run/yufeng/modelside.sock`。确认套接字存在且只允许 `yufeng` 运行用户访问：

```sh
sudo test -S /run/yufeng/modelside.sock
sudo stat /run/yufeng/modelside.sock
```

跨主机监听时，把 `YUFENG_MODELSIDE_LISTEN` 改为 HTTPS 地址，并在服务命令中追加 `--listen-ca`、`--listen-cert`、`--listen-key`。服务端证书必须覆盖 Edge 实际使用的名称，`--listen-ca` 只信任获准入口主机的客户端证书权威。

`YUFENG_MODELSIDE_INGRESS_CAPACITY=0` 使用推理线程数两倍的默认批次交接槽。跨主机突发导致 `ingress_dropped` 增长时，可按容量测试结果设置 2–64；该参数只增加易失批次交接容量，不改变 Edge 的模型输入缓存窗口，也不能替代扩展推理能力。

## 4. 升级与回退

升级 wheel 或 TensorFlow 运行时前记录虚拟环境、权重摘要、ModelSide 身份和控制台最近结果时间。把新 wheel 安装到新的虚拟环境，验证 `--help`、权重清单和依赖导入后再切换 systemd 的可执行路径。不要覆盖正在使用的权重文件。

```sh
sudo python3 -m venv /opt/yufeng/modelside-next
sudo /opt/yufeng/modelside-next/bin/pip install './wheels/yufeng_modelside-0.1.2-py3-none-any.whl[tensorflow]'
sudo /opt/yufeng/modelside-next/bin/yufeng-modelside --help
```

若新服务无法加载签名权重、无法接收 Edge 批次、结果队列持续丢弃或控制台不再出现模型结果，恢复上一虚拟环境和未变更权重目录后重启。ModelSide 回退不会改变 Edge 的同步检测与 Gate，也不会要求 Brain 反向连接。
