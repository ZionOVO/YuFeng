# 外部 Agent 监督进程安装

`yufeng-agentd` 和 `yufeng-run` 可以部署在 Linux x86-64 / 64 位 ARM、Windows x86-64 与 macOS x86-64 / 64 位 ARM 主机。Brain 不会反向连接该主机；客户端始终主动连接 Brain。

## 注册与激活

1. 解压与主机平台相符的发布包，准备 Brain 的 HTTPS 地址和信任根证书。
2. 以管理员权限运行发布包 `agentd/` 下对应平台的安装脚本。脚本在最终的受保护状态目录生成客户端私钥、证书请求和 X25519 激活密钥，提交注册请求，然后安装并启动监督服务。
3. 管理员在控制台 `/cases` 选中唯一资产，核对主机、公钥指纹、操作系统、处理器架构与沙箱能力后批准注册。浏览器不会取得证书或一次性引导令牌的明文。
4. 服务以 `-activate` 启动并轮询 X25519 加密激活包；该包只对本机公钥加密。本机解密、校验证书绑定并成功建立首次会话后，先持久化 `worker-refresh`，再确认并销毁临时材料；后续重启仍保留 `-activate`，但检测到有效刷新状态后不会重新领取一次性材料。

Linux：

```text
sudo agentd/install-linux.sh https://brain.example:9050 site-a-agentd /path/brain-ca.pem
```

macOS：

```text
sudo agentd/install-macos.sh https://brain.example:9050 site-a-agentd /path/brain-ca.pem
```

Windows PowerShell（以管理员身份运行）：

```powershell
agentd\Install-Windows.ps1 -Brain https://brain.example:9050 -Worker site-a-agentd -TLSCA C:\secure\brain-ca.pem
```

## 安全边界

- 证书私钥在客户端本机生成，不会进入激活包或 Brain。工作负载证书有效期为 24 小时，到期前由客户端主动轮换。
- 注册广告只参与平台兼容调度，不会自动产生资产 Bindings（资源绑定边界）。
- Linux 需要 Landlock、seccomp 与资源限制；macOS 需要可验证的受限沙箱配置。Windows 目前已实现命名管道、受限令牌和作业对象，但在 AppContainer（应用容器）未验证前不广告完整沙箱能力，Brain 不会向它派发流量调查。
- 任何非调查型执行都只能调度到通过 Linux 强沙箱门禁的 worker。
