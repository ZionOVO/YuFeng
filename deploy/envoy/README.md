# Envoy 外部授权接入

适用前提：客户现有 Envoy 已终止业务传输层安全协议（Transport Layer Security，TLS），御锋部署引导已选择“Envoy 外部授权”，并为该单元签发外部授权监听地址。样例只处理解密后的超文本传输协议（Hypertext Transfer Protocol，HTTP），不读取业务证书或私钥。

## 接线

1. 在控制台部署步骤选择“Envoy 外部授权”，填写稳定流量键、边缘监听地址和 Envoy 直接对端所在的精确可信代理网段。该姿态不填写业务上游。
2. 把 [`envoy.yaml`](envoy.yaml) 的 `yufeng-edge-local:18081` 替换为边缘监听地址，把 `application:8080` 替换为原业务集群；将外部授权过滤器放在现有路由过滤器之前。现场保留原监听器的证书、私钥、路由和应用集群，不复制样例的明文 `:10000` 监听器。
3. 先验证配置，再重新加载 Envoy。固定样例使用 Envoy 1.38.0；升级版本必须重跑本目录集成门禁。

容器校验命令：

```sh
docker run --rm -v "$PWD/deploy/envoy/envoy.yaml:/etc/envoy/envoy.yaml:ro" envoyproxy/envoy:v1.38.0 --mode validate -c /etc/envoy/envoy.yaml
```

过滤器将方法、路径、查询、`Host`、`Content-Type`、`X-Forwarded-For`、`X-Forwarded-Proto`、网关生成的 `X-Request-Id` 和最多 64 KiB 请求体交给边缘；不把业务 `Authorization` 或 `Cookie` 交给授权服务。正文超过上限时只发送前缀并标记部分覆盖，边缘返回 403。

边缘内部检查超过 50ms 会主动返回 200，保留单次失败开放。Envoy 等待授权服务 100ms，且 `failure_mode_allow` 必须为 `false`：边缘熔断返回 503、授权服务断连或 Envoy 自身等待超时都会在网关返回 503，并且不访问业务应用。

## 自动验收

以下命令启动固定版本 Envoy、真实 `ExtAuthz` 处理器和回显应用，自动断言必需头、敏感头排除、正文缺失、正文部分覆盖、200、403、单次超时放行、熔断 503、半开恢复和授权服务断连：

```sh
make envoy-live
```

该门禁约需一分钟，其中 42 秒用于验证真实的 10 秒滑动窗和 30 秒恢复保持时间。

## 回退

恢复接入前的 Envoy 监听器配置并重新加载，移除外部授权过滤器，使流量继续走原业务集群。这一次配置回滚就是完整回退；不调用中台、不改资产世代、不把御锋改成业务传输层安全协议终止点。
