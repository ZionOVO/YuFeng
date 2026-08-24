# Edge 人工生命周期

`yufeng-edge` 只能由技术人员安装、启动、升级和卸载。Brain 与贾维斯不会反向连接本机，也不会创建、重启或删除该服务。Edge 启动后主动连接 Brain，注册单元并拉取已经签名的监听计划、资产世代与检测策略。

## 原生 Linux 服务

1. 管理员先在控制台 `/app/setup` 提交部署规格，记录返回的 `unit_id`、监听计划版本与资产世代。
2. 把发布包中的 `yufeng-edge` 与本目录复制到目标主机，执行：

   ```sh
   sudo ./edge/install-linux.sh install ./bin/yufeng-edge
   ```

3. 编辑 `/etc/yufeng/edge.env`。把单元引导令牌、制品验签公钥、来源假名密钥和 Edge 客户端相互传输层安全协议材料分别放到服务文件声明的路径，所有私钥与令牌权限设为 `0640 root:yufeng`。
4. 执行 `sudo systemctl start yufeng-edge`，再在控制台确认 Edge 已装载部署规格中的监听计划与世代。

升级与卸载由技术人员显式执行：

```sh
sudo ./edge/install-linux.sh upgrade ./bin/yufeng-edge
sudo ./edge/install-linux.sh uninstall ./bin/yufeng-edge
```

卸载默认保留 `/etc/yufeng` 和 `/var/lib/yufeng`，便于人工审计或回滚；确认不再需要后再由技术人员单独清理。

## 容器与远端 Brain

同机控制面与边缘使用：

```sh
docker compose -f deploy/compose.yaml up -d --build
YUFENG_EDGE_UNIT=site-a-edge \
YUFENG_MODELSIDE_ID=site-a-modelside \
YUFENG_MODELSIDE_WEIGHTS_DIR=/srv/yufeng/models \
docker compose -f deploy/compose.yaml -f deploy/compose.edge-modelside.yaml up -d --build edge modelside
```

独立主机使用同一 `compose.edge-modelside.yaml`，但必须设置 `YUFENG_BRAIN_URL=https://brain.example:9050`，并先把控制面生成或企业公钥基础设施签发的 `yufeng_pubkey`、`yufeng_source`、`yufeng_tls_edge`、`yufeng_tls_modelside` 四个卷安全地预置到该主机。不得把证书私钥、来源假名密钥或引导令牌写入环境变量。跨主机的 Brain 证书必须覆盖实际域名。

Edge 与 ModelSide 分机时，把 `YUFENG_MODELSIDE_ENDPOINT` 改为 `https://modelside.example:9443`，同时为 Edge 配置 `YUFENG_MODELSIDE_TLS_CA`、`YUFENG_MODELSIDE_TLS_CERT` 与 `YUFENG_MODELSIDE_TLS_KEY`；ModelSide 监听端必须要求客户端证书，防火墙只允许同一受控防御网络。
