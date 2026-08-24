# 客户入口接入反向代理

适用前提：客户入口已终止业务传输层安全协议（Transport Layer Security，TLS），能把选定域名或路由的解密后超文本传输协议（Hypertext Transfer Protocol，HTTP）流量转给 `yufeng-edge`，并保留原业务应用回源配置。

## 配置

1. 将 [`nginx-site.conf.example`](nginx-site.conf.example) 的域名、证书路径、`yufeng-edge` 地址和应用健康路径替换为现场值；把客户入口所在精确网段填入御锋部署表单的可信代理网段。
2. 在入口加载前执行 `nginx -t`。经御锋访问应用健康路径，确认普通请求成功、上游 4xx/5xx 原样返回、已生效策略的攻击请求为 403。
3. 提交入口配置，把选定流量切到 `yufeng_backend`。保留提交前的入口配置版本。

入口必须保留 `Host`，追加 `X-Forwarded-For`，并透传升级头。御锋不可达或过载时不切到未检查路径：连接失败由入口返回 502，同步在途超限由御锋返回 503。

## 回退

恢复入口提交前的回源池配置并重新加载，使选定流量直接回到原业务应用。这一次配置回滚就是完整回退；不调用中台、不改资产世代、不等待数据面监督器。

NGINX 命令示例：

```sh
nginx -t && nginx -s reload
```
