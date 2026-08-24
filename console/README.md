# 控制台

御锋 2.0 控制台前端采用 Vite、React 19、TypeScript、HeroUI 与 Tailwind 层叠样式表。交付时由 `yufeng-brain` 托管在 `/app`（[`docs/api.md`](../docs/api.md) 第 17.1 节）；开发期仍可使用 Vite 服务器。六步初次配置页、权限授予页、提案意图、会话与幂等键复用均已实现。

协议与实现名词见[术语表](../docs/glossary.md#protocol-and-implementation-terms)。

## 常用命令

```bash
npm install        # 安装依赖（锁文件为 package-lock.json）
npm run dev        # 开发服务器（经 Vite 代理连接本地真实中台）
npm run build      # 类型检查 + 产物构建（dist/，运行时只连接真实中台）
npm run test       # vitest 单元/页面测试
npm run lint       # eslint
npm run typecheck  # tsc -b
```

## 运行与测试边界

页面只依赖 `src/api/client.ts` 的 `ConsoleClient` 类型化接口，`src/api/index.ts` 始终装配 `ConnectClient`。开发期 `/yufeng` 由 Vite 代理到 `VITE_BRAIN_URL`（默认 `https://127.0.0.1:9050`，开发代理接受本机自签名证书）；运行 `-dev-insecure` 明文 Brain 时显式设置 `VITE_BRAIN_URL=http://127.0.0.1:9050`。没有本地中台时页面应显示真实连接错误，不会回退到内置数据。

`ConnectClient` 当前手写 Connect JSON 请求，是过渡适配层；[Connect-ES](../docs/glossary.md#connect-es)（Connect 协议的 TypeScript 客户端生成与运行库）已选定但尚未引入。生成客户端落地后只替换该适配层内部，`ConsoleClient` 与页面保持不变。

组件测试从 `src/test/` 注入场景夹具，只验证页面呈现和交互；夹具不进入运行时依赖图，也不作为服务端认证、授权和业务状态机的权威测试。真实服务行为由 Brain 的 PostgreSQL 集成测试覆盖。

页面按钮看 `GetMe.access`（动作清单 × 对象绑定），不看角色名猜权限（[`docs/api.md`](../docs/api.md) 第 17.2 节）。授予应用程序编程接口：`listGrants` / `putGrant` / `revokeGrant`。

交付源码与构建产物不包含设计回廊或固定业务数据；视觉主题只保留正式控制台使用的 `fusionr` 配置。

## 目录结构

```
src/
  api/            适配层：types（protojson 薄类型）/ errors / client（接口）/ connect
  auth/           会话（sessionStorage，键 yufeng.session）与 AuthContext
  components/     AppShell、路由守卫、StateView/ConfirmDialog 等共享件、useAsyncData
  pages/          login / dashboard / assets / events / releases / audit / users / model / agent
  theme/          fusion.css（圆融主题变量）与 app.css（结构件类）
  test/           测试脚手架（MemoryRouter、AuthProvider 与 ConsoleClient 场景夹具）
```

## 约定

- 会话令牌只存 sessionStorage；收到 unauthenticated 清会话并由路由守卫送回 /login；permission_denied 渲染无权限状态。
- 角色控制（viewer 只读、/users 仅管理员）只是用户体验，不构成安全边界（鉴权以服务端为准）。
- 页面不直接拼接 URL 或发起 fetch；一切数据访问经过 ConsoleClient。
- 设计主题「御锋安全运营中心·圆融」：HeroUI 原生圆角与深色安全运营色板，作用于 `.fusionr` 样式范围。
