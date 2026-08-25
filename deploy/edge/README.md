# Edge 人工接入与生命周期

`yufeng-edge` 只能由技术人员安装、启动、升级、回退和卸载。Brain 与贾维斯不会反向连接入口主机，也不会创建、重启、删除或探测该服务。Edge 启动后主动连接 Brain，注册单元并拉取已经签名的监听计划、资产世代与检测策略。

## 1. 在控制台登记

1. 完成 `/app/setup` 的模型网关探测与贾维斯在线确认，进入主控制台；
2. 在 `/app/assets` 登记真实被保护资产；
3. 打开资产详情，选择“接入 Edge”，填写入口姿态、单元标识、监听地址、真实上游、流量键、可信代理网段、模型档案和模型输入缓存窗口；
4. 记录控制台返回的 `unit_id`、确定性 ModelSide 身份、期望监听计划版本、期望资产世代标识与序号。相同规范摘要不会重复签发；配置变化才产生新坐标。

“接入 Edge”只签发配置制品，不产生令牌、私钥或进程动作。页面不会读取 Edge 管理口。

## 2. 所需文件

技术人员通过受控渠道把下列文件放到入口主机，令牌与私钥不得写入环境变量、命令历史或工单正文：

- 发布清单校验通过的 `yufeng-edge` 二进制、本目录的 systemd 物料；
- 单元首次注册引导令牌；
- Brain 服务端证书的信任根、Edge 客户端证书和私钥；
- 制品签名公钥 `artifact-pubkey.hex`；
- 32 字节来源假名密钥；
- 若跨主机连接 ModelSide，再提供独立的 ModelSide 信任根、Edge 到 ModelSide 的客户端证书和私钥。

文件建议放在 `/etc/yufeng/credentials`、`/etc/yufeng/tls` 和 `/etc/yufeng/trust`，目录权限 `0750 root:yufeng`，令牌、私钥和来源假名密钥权限 `0640 root:yufeng`。

## 3. 原生 Linux 服务

安装二进制与服务文件：

```sh
sudo ./edge/install-linux.sh install ./bin/yufeng-edge
sudo install -m 0640 -o root -g yufeng deploy/edge/edge.env.example /etc/yufeng/edge.env
```

编辑 `/etc/yufeng/edge.env`：

- `YUFENG_BRAIN_URL` 使用实际 Brain HTTPS 地址；
- `YUFENG_EDGE_UNIT` 必须与资产详情中的接入单元标识完全一致；
- `YUFENG_MODELSIDE_ENDPOINT` 同机使用 `unix:///run/yufeng/modelside.sock`，分机使用受控网络中的 HTTPS 地址；
- `YUFENG_MODEL_INGRESS_WINDOW_MAX_ITEMS`、`YUFENG_MODEL_INGRESS_WINDOW_MAX_BYTES`、`YUFENG_MODEL_INGRESS_WINDOW_MAX_AGE` 是本机硬上限；Brain 签名的期望只能逐项收窄，不能放大。

启动并检查：

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now yufeng-edge
sudo systemctl status --no-pager yufeng-edge
curl --fail --silent --show-error http://127.0.0.1:19092/ready
```

控制台中的 Edge 状态应从“等待注册”变为“在线且收敛”。“制品未收敛”表示心跳存在，但实际监听计划或资产世代与期望坐标不同；“离线”表示最近心跳超出窗口。验收时还要从客户入口发送健康请求，确认反向代理实际到达填写的上游。

## 4. 升级、回退与卸载

升级前保存当前二进制摘要、`/etc/yufeng/edge.env`、本地缓存目录坐标和控制台中的期望/实际版本。安装新二进制后只重启 Edge，不修改 Brain、资产或签名制品：

```sh
sha256sum /usr/local/bin/yufeng-edge
sudo ./edge/install-linux.sh upgrade ./bin/yufeng-edge
sudo systemctl restart yufeng-edge
curl --fail --silent --show-error http://127.0.0.1:19092/ready
```

若新进程不能注册、不能装载现有签名坐标、入口返回异常或健康检查失败，恢复上一份已校验二进制并再次重启。回退不删除 `/var/lib/yufeng/edge` 与 `/var/lib/yufeng/telemetry`，Edge 会继续使用最近一份已验证制品。

```sh
sudo install -m 0755 ./rollback/yufeng-edge /usr/local/bin/yufeng-edge
sudo systemctl restart yufeng-edge
```

卸载由技术人员显式执行：

```sh
sudo ./edge/install-linux.sh uninstall ./bin/yufeng-edge
```

卸载默认保留 `/etc/yufeng` 和 `/var/lib/yufeng`，便于审计或回退。确认现场变更单关闭后再单独清理；Brain 只把该接入投影为离线，不会代为删除主机数据。

## 5. 容器与远端 Brain

同机控制面与数据面使用：

```sh
YUFENG_EDGE_UNIT=site-a-edge \
YUFENG_MODELSIDE_ID=site-a-edge-modelside \
YUFENG_MODELSIDE_WEIGHTS_DIR=/srv/yufeng/models \
docker compose -f deploy/compose.yaml -f deploy/compose.edge-modelside.yaml up -d edge modelside
```

独立入口主机可以只使用 `compose.edge-modelside.yaml`，但必须设置 `YUFENG_BRAIN_URL=https://brain.example:9050`，并先安全预置制品签名公钥、来源假名密钥、Edge 客户端传输层安全材料、ModelSide 客户端传输层安全材料和两个专用令牌文件。跨主机 Brain 证书必须覆盖实际域名或互联网协议地址。

Edge 与 ModelSide 分机时，把 `YUFENG_MODELSIDE_ENDPOINT` 改为 `https://modelside.example:9443`，同时设置 `YUFENG_MODELSIDE_TLS_CA`、`YUFENG_MODELSIDE_TLS_CERT` 与 `YUFENG_MODELSIDE_TLS_KEY`。ModelSide 监听端必须验证 Edge 客户端证书，防火墙只允许精确入口主机访问。Edge 到远端 ModelSide 的网络可由受控专网、私有覆盖网络或运维隧道提供；普通公网明文连接不合格。
