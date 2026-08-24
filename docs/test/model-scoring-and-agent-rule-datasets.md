# Edge 邻近模型打分与智能代理规则提炼数据集

本文规定公开超文本传输协议（HTTP）材料怎样进入 0.2.0 的两条测试链路：

1. Edge 把已解密请求规范化后，通过有界非阻塞队列交给邻近 `yufeng-modelside` 推理。
2. Brain 接收类型化 `MODEL_ALERT` 或 `REVIEW_SAMPLE`，聚合案件；需要研判时，贾维斯只看冻结的脱敏检查票据并给出治理建议。

公开材料下载到仓库根的 `testdata/traffic-datasets/`，该目录不入库。一键拉取使用 `./scripts/fetch-traffic-datasets.sh`。权威语义见 [`architecture.md`](../architecture.md) 第 4 节、[`api.md`](../api.md) 第 21.5 节和 [`design.md`](../design.md) 第 4 节。

## 1. 实际数据路径

```text
公开 HTTP 请求
      │ 回放到反向代理、外部授权或已解密复制入口
      ▼
yufeng-edge
      ├─ 同步：规范视图 → Inspector → Gate → 当前请求结束
      └─ 异步：NormalizedTraffic → 有界非阻塞队列
                                      │
                                      ▼
                              yufeng-modelside
                              TensorFlow 推理与签名策略采样
                                      │
                          MODEL_ALERT / REVIEW_SAMPLE
                                      ▼
                                 yufeng-brain
                    事件、推理、票据、案件与事务发件箱
                                      │
                              需要时才叫醒贾维斯
```

ModelSide 可见签名档案允许的请求头、查询参数与有界正文，因此公开数据必须能还原为方法、路由、查询、头、正文和内容类型。网络流特征表、抓包文件、未重组的传输控制协议字节和加密流量不能直接进入本链路。

Brain 与智能代理看不到 `NormalizedTraffic` 或完整正文。Brain 只接收结果分数、类型、模型引用、覆盖度与无敏感值的路由信息，并按历史签名世代冻结检查票据。模型告警可以唤醒研判；复核样本只进入有界案件聚合，不逐条叫醒贾维斯。

## 2. 基础数据约束

回放适配器必须：

- 为每条请求生成唯一 `request_id`，并绑定测试单元、资产和当前世代；
- 保留重复查询参数和规范顺序，路由字段改成不含敏感值的模板；
- 只转交模型档案 `allowed_headers` 列出的规范小写请求头；
- 记录原始正文长度，并在超过 `max_body_bytes` 时截断且设置 `body_truncated=true`；
- 按实际入口填写方法、路由、内容类型与逐检查面覆盖度；
- 不携带 Cookie、Authorization、客户端原始地址、业务传输层安全协议密钥或任何平台凭据；
- 不把数据集标签、期望规则编号或训练答案混入生产请求字段。

同一份样本的标签只用于离线比对，不能由 Edge 或 ModelSide 当成推理输入。若适配器需要把 CSV 或生成器 YAML 展开为 HTTP，请把展开器与期望结果固定在测试代码中，确保回放可重复。

## 3. 仓库内优先语料

先使用 `procedures/http-inspection-baseline/corpus/`：

| 文件 | 用途 |
|---|---|
| `malicious.jsonl` | 带已知攻击类别与期望规则前缀；验证同步检测、ModelSide 分数和模型告警入账 |
| `benign.jsonl` | 验证低分不入账、复核采样上限与误报率 |
| `parse-diff.jsonl` | 验证畸形、截断和覆盖不足；不要把解析失败自动标成模型漏检 |

仓库语料已经表达方法、路径、查询、正文和内容类型，适合先打通完整链路。公开集用于增加载荷、规则编号、路由和正文形态的多样性。

## 4. 公开材料选择

| 优先级 | 材料 | 使用方式 | 排除项 |
|---|---|---|---|
| 必下 | OWASP 核心规则集 4.25.0 回归 YAML | 只用已装载的 930、931、932、934、941、942 系列中 paranoia 1 用例，展开成真实 HTTP | paranoia 2–4、响应规则、扫描器和未装载规则系列 |
| 选下 | HttpParamsDataset | 把 `payload_full.csv` 中载荷编码为查询参数或表单正文；标签只作离线期望 | 直接把 CSV 传给 ModelSide；把标签混入请求 |
| 选下 | CSIC 2010 原始 HTTP 文本 | 使用正常训练/测试流量和异常测试流量补充完整 HTTP 形态 | Weka、ARFF 和预计算特征表 |
| 选下 | WAFFLED 公开原始绕过样本 | 用原始字节转发器验证规范化、截断与覆盖度 | 用 curl 重写畸形包；把生成语法当成现成请求 |
| 选下 | GoTestWAF 测试生成器 | 按其编码器和放置位展开后再回放 | 直接发送 YAML、原生 gRPC、128 KiB 正文作为普通样本 |
| 禁止 | CIC-IDS、UNSW-NB15、NSL-KDD 特征 CSV | 无 | 这些数据不含本契约所需的规范 HTTP 字段 |

## 5. 一键下载

需要 `git`、`curl` 和 `python3`：

```sh
./scripts/fetch-traffic-datasets.sh
```

默认写入 `testdata/traffic-datasets/`。也可指定绝对目录：

```sh
YF_TRAFFIC_DATASETS_DIR=/abs/path ./scripts/fetch-traffic-datasets.sh
```

脚本会生成 `MANIFEST.txt`，并取得：

```text
testdata/traffic-datasets/
  MANIFEST.txt
  crs-4.25.0/
    rules/
    tests/
    pl1/
  http-params/payload_full.csv
  csic-2010/*.txt
  waffled/
  gotestwaf-cases/
```

核心规则集提交必须和 `procedures/http-inspection-baseline/core-rule-set-manifest.json` 一致。脚本从规则标签计算 paranoia 1 闭集并写入 `pl1/`；测试不得把上游全部 YAML 当成本产品的加载面。

## 6. 各数据集的展开规则

### 6.1 OWASP 核心规则集

上游：<https://github.com/coreruleset/coreruleset>，固定标签 `v4.25.0`。YAML 使用 go-ftw 格式，字段通常包括 `method`、`uri`、`headers` 和 `data`；它不是已编码的套接字流量。

- 正向期望读取 `output.log.expect_ids`；`no_expect_ids` 是阴性断言。
- 有 `data` 而没有 Content-Type 时，回放适配器应按上游测试器规则补 `application/x-www-form-urlencoded`。
- `encoded_request` 或关闭自动补头的用例必须走原始字节转发器，不能由高级 HTTP 客户端重写。
- 无强制策略时，同步核心规则集命中仍可能返回 200；测试重点是发现、模型结果和后续治理，不应把“没有 403”误判成失败。

### 6.2 HttpParamsDataset

上游：<https://github.com/Morzeux/HttpParamsDataset>。`payload_full.csv` 的载荷不是完整 HTTP，至少应展开成：

```text
GET /search?q=<urlencode(payload)> HTTP/1.1
Host: app.example
```

也可生成带正确 Content-Type 的表单正文，用来验证正文、长度和截断字段。`label` 与 `attack_type` 只用于测试期望；不进入 `NormalizedTraffic`。超出签名正文上限的样本应验证 `body_truncated`，而不是静默丢失长度信息。

### 6.3 CSIC 2010

推荐镜像：<https://github.com/msudol/Web-Application-Attack-Datasets>。只使用 `OriginalDataSets/csic_2010/` 中三份原始 HTTP 文本：

- `normalTrafficTraining.txt`
- `normalTrafficTest.txt`
- `anomalousTrafficTest.txt`

回放时把 Host 改成监听计划保护的资产主机，并保持方法、路径、头与正文语义。异常文件不是逐条可靠攻击金标；需要按实际同步发现与离线模型期望进一步筛选。

### 6.4 WAFFLED

论文：<https://arxiv.org/abs/2503.10846>；代码：<https://github.com/sa-akhavani/waffled>。公开 `bypass-database/` 样本用于验证解析差与覆盖度，必须由 `http-request-relay` 保持原始字节。只有另行确认上游仍执行攻击的样本，才可作为漏检对照；单纯“未返回 403”不是漏检证明。

### 6.5 GoTestWAF

上游：<https://github.com/wallarm/gotestwaf>。`testcases/` 描述载荷、编码器和放置位的组合，需要先展开为请求。优先选择 URL 参数、表单、JSON 和允许请求头；原生 gRPC 不在本契约范围，128 KiB 样本只用于正文容量与截断测试。

## 7. 建议实验矩阵

每次实验固定资产世代、模型档案摘要和模型权重版本，避免把策略或模型漂移混入结果：

| 数据 | 主要断言 |
|---|---|
| 良性请求 | 低于复核下限时无结果；满足新路由条件时只产生有界 `REVIEW_SAMPLE` |
| 同方法、同路由的多条风险样本 | 五分钟窗只保留最高风险代表；每单元最多四条 |
| 分数达到告警阈值 | 每条均全量尝试 `MODEL_ALERT`；Brain 重试入账仍幂等 |
| 超长正文 | ModelSide 只看有界正文；原始长度和截断状态正确 |
| 禁止请求头 | 不出现在 ModelSide 输入、日志、结果或 Brain 数据库 |
| ModelSide 输入满载 | Edge 立即丢旁路并计数；同步动作和延迟预算不变 |
| Brain 断连或磁盘变慢 | 本地推理继续；结果队列独立重试或有界丢弃 |
| 同一结果重复上传 | Brain 只创建一组事件、推理、票据、案件和发件箱记录 |
| `REVIEW_SAMPLE` 批量上传 | 只聚合案件，不逐条创建或唤醒贾维斯研判 |

模型质量指标至少按模型版本报告精确率、召回率、接收者操作特征曲线下面积和各攻击类别混淆矩阵；系统指标单独报告两条队列的接受、丢弃、重试和延迟。不能用系统丢弃掩盖模型漏检，也不能用模型准确率替代 2000 请求/秒与第 99 百分位延迟验收。

## 8. 禁止做法

- 把 CSV、YAML、数据集标签或预计算特征直接当作生产请求契约。
- 让 ModelSide 读 Edge/Brain 日志、Redis、PostgreSQL、消息服务器或执行实例任务。
- 让 Brain、贾维斯或调查执行实例读取规范化原始流量。
- 让模型分数直接改变当前请求，或直接生成未经治理的任意正则。
- 为了 Brain、智能代理或第二个模型消费者再次复制完整正文。
- 把流量审查候选、模型复核样本和智能代理研判指令混成同一队列。
- 用交换机镜像或加密抓包冒充已解密 HTTP 输入。
