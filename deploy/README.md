# 部署物料

本目录把控制面与人工管理的数据面明确分开：

- `compose.yaml` 只启动 PostgreSQL、流量入账受限角色、密钥初始化器、签名器、Brain、贾维斯与中央 agentd；它不启动 Edge、ModelSide 或数据面监督器，也不挂载 Docker 套接字；
- `compose.edge-modelside.yaml` 由技术人员显式启动、升级和停止 `yufeng-edge + yufeng-modelside`，同机使用权限受限的 Unix 域套接字；
- `edge/` 提供原生 Go 二进制的 Linux systemd 服务、安装、升级和卸载物料；
- `modelside/` 提供原生 Python 包的 Linux systemd 服务物料；
- `compose.test.yaml` 只供验收，启动两个可区分的回显应用；
- `reverse-proxy/` 与 `envoy/` 分别给出反向代理和外部授权入口的参考配置与回退步骤；
- `pilot-change-record.md` 是现场版本、网络、责任人、切换、回退、轮换和恢复证据的必填模板。

## 控制面

正式 Compose 不携带默认口令或引导令牌。启动前复制 `deploy/.env.example` 到仓库根目录 `.env`，按 [`secrets/README.md`](secrets/README.md) 创建所有独立随机凭据，然后执行：

```sh
docker compose -f deploy/compose.yaml up -d --build
```

`keys` 幂等生成签名密钥、Edge 来源假名密钥和各进程的相互传输层安全协议材料。Brain 只取得服务端私钥、签名套接字、单元引导令牌和 ModelSide 结果令牌；贾维斯与 agentd 不取得 Edge、ModelSide、来源假名或 Docker 权限。

## 0.1.0 交付物

发布工作流分别交付原生 Edge 二进制与 systemd 物料、`yufeng-modelside` Python wheel 与 systemd 物料、Compose 部署包，以及 Linux amd64 的 Edge 和 ModelSide 容器镜像归档。容器目标主机可以先校验 `yufeng-v0.1.0-checksums.txt`，再显式载入镜像：

```sh
gzip -dc yufeng-v0.1.0-edge-image-linux-amd64.tar.gz | docker load
gzip -dc yufeng-v0.1.0-modelside-image-linux-amd64.tar.gz | docker load
export YUFENG_EDGE_IMAGE=yufeng-edge:v0.1.0
export YUFENG_MODELSIDE_IMAGE=yufeng-modelside:v0.1.0
```

加载镜像不会启动服务；后续生命周期仍由技术人员执行下面的 Compose 命令。非 amd64 主机使用原生 Edge 交付物，并按目标 Python 与 TensorFlow 平台安装 ModelSide wheel，除非对应平台另有经过校验的容器镜像。

## 同一物理节点的 Edge 与 ModelSide

管理员先在 `/app/setup` 提交部署规格。技术人员再明确提供单元、ModelSide 身份和权重目录，并把扩展 Compose 与控制面 Compose 一起启动：

```sh
YUFENG_EDGE_UNIT=site-a-edge \
YUFENG_MODELSIDE_ID=site-a-modelside \
YUFENG_MODELSIDE_WEIGHTS_DIR=/srv/yufeng/models \
docker compose -f deploy/compose.yaml -f deploy/compose.edge-modelside.yaml up -d --build edge modelside
```

停止或升级也必须由技术人员显式运行 Docker Compose。Brain 和贾维斯不会调用这些命令。Edge 启动后主动注册并拉取签名监听计划、资产世代和模型档案；ModelSide 只接收 Edge 的规范流量并向 Brain 上报无原文结果。

## 独立或远端部署

`compose.edge-modelside.yaml` 也可在独立主机运行。设置真实 `YUFENG_BRAIN_URL`，通过安全交付预置制品验签公钥、来源假名密钥、Edge 客户端证书和 ModelSide 客户端证书对应的四个 Docker 卷，再运行相同命令。生产 Brain 地址必须使用 HTTPS，客户端证书与服务端域名必须由实际公钥基础设施正确签发。

Edge 与 ModelSide 分机时不使用这份同机 Unix 套接字编排：分别启动容器或原生服务，Edge 到 ModelSide 改用 HTTPS 双向传输层安全协议，ModelSide 到 Brain 继续使用独立 HTTPS 双向传输层安全协议；两条连接都必须由防火墙限制在同一受控防御网络。原始头、查询参数和正文不得发送给 Brain。

## 交付验证

软件 Release 下载后先按 `yufeng-v0.1.0-release-manifest.json` 与 `yufeng-v0.1.0-checksums.txt` 复核实际文件，再在目标环境运行 `scripts/delivery-evidence.sh static` 和所需的活栈、恢复、容量诊断。部署验收必须覆盖旁路关闭、ModelSide 空闲、ModelSide 满载、Brain 断连和磁盘变慢下每秒 2000 个请求及第 99 百分位延迟预算，并记录客户现场的代理网段、上游、证书、网络核对和变更责任人。部署结果不覆盖 Release 资产，也不改变软件已经发布的事实。
