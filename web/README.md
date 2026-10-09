# AegisDNS / DNSForge 前端

DNSForge 管理面板 —— 多租户加密 DNS 服务（DoT / DoH / 普通 DNS）的 Web 控制台。

技术栈：**React 18 · TypeScript 5 · Vite 5 · Tailwind CSS 3.4 · shadcn/ui 风格组件（Radix UI 原语手写）· TanStack Query 5 · React Router 6 · Recharts 2 · Framer Motion 11**

---

## 目录结构

```
web/
├── index.html              # 入口 HTML（含防主题闪烁脚本）
├── vite.config.ts          # base: './'，产物相对路径引用，便于 go:embed
├── tailwind.config.js      # 设计令牌映射（颜色 / 圆角 / 字体 / 阴影）
├── postcss.config.js
├── tsconfig.json
└── src/
    ├── main.tsx            # 入口，加载本地字体与全局样式
    ├── App.tsx             # 路由与 Provider 组合
    ├── api/                # axios 实例 + 拦截器 + 各模块 REST 封装 + 类型
    ├── components/
    │   ├── ui/             # 手写 shadcn/ui 组件（button/input/dialog/...）
    │   ├── layout/         # Sidebar / BottomTabBar / TopBar / 布局
    │   └── ...             # StatCard / PageHeader / ConfirmDialog 等
    ├── hooks/              # useAuth / useTheme / useMediaQuery / useCopy
    ├── lib/                # cn() 与格式化工具
    ├── pages/              # 各页面
    └── styles/globals.css  # CSS 变量（浅色/暗色主题）
```

## 开发

前置：Node ≥ 18、pnpm（本项目使用 pnpm，勿用 npm/yarn）。

```bash
cd web
pnpm install          # 安装依赖

# 启动开发服务器（默认 http://localhost:5173）
pnpm dev
```

开发模式下 Vite 会把 `/api` 反向代理到 `http://127.0.0.1:8080`（apid 默认端口），
因此本地开发时请确保 apid 正在运行，或修改 `vite.config.ts` 中的 `server.proxy` 目标。

## 构建

```bash
pnpm build            # 先 tsc 类型检查，再 vite build，产物输出到 web/dist/
pnpm build:fast       # 跳过类型检查，仅打包（应急用）
pnpm preview          # 本地预览构建产物
pnpm typecheck        # 仅做类型检查
```

构建产物位于 `web/dist/`，其中 `dist/index.html` 是入口。

### 与后端集成

- 产物使用**相对路径**（`base: './'`），由 Go 的 `go:embed` 内嵌进 `apid`，并在 `/` 下作为 SPA 提供，因此**无需 CORS**。
- 生产构建**不生成 source map**。
- 前端与后端通过 `/api/v1` 下的 REST API 通信，认证使用 `Authorization: Bearer <token>`。
  - `access_token` 存内存，`refresh_token` 存 localStorage（30 天）。
  - axios 响应拦截器在收到 401 时自动刷新令牌并重放原请求；并发请求共享同一个刷新 Promise，避免重复刷新导致的令牌轮转冲突。
- apid 需对非 `/api` 的路径回退到 `index.html`（SPA history fallback），以支持前端路由（React Router BrowserRouter）。

## 设计规范要点

- **主题**：CSS 变量驱动的浅色 / 暗色主题，主色 `#6366F1`；主题偏好持久化在 `localStorage`（键名 `aegis-theme`）。
- **响应式**：`< 768px` 使用底部 TabBar，`>= 768px` 使用左侧 Sidebar；统计卡 `1 → 2 → 4` 列。
- **可访问性**：触摸目标 ≥ 44px；移动端输入框字号 ≥ 16px（防 iOS 缩放）；所有动效尊重 `prefers-reduced-motion`。
- **字体**：`Inter` + `PingFang SC` + `Microsoft YaHei`；域名/地址等使用 `JetBrains Mono`，数字使用 `tabular-nums`。

## 页面清单

| 路由 | 页面 | 说明 |
|---|---|---|
| `/` | Landing | 落地页 |
| `/login` | Login | 登录 |
| `/register` | Register | 注册（成功后展示专属地址） |
| `/app` | Dashboard | 总览：统计卡 + 趋势图 + 拦截 TOP |
| `/app/setup` | DnsSetup | 我的 DNS 配置（地址 / 二维码 / 分平台指引） |
| `/app/rules` | Rules | 规则管理（增删改查 / 批量导入 / 批量删除） |
| `/app/subscriptions` | Subscriptions | 订阅管理（含推荐订阅） |
| `/app/stats` | Stats | 统计分析（时间序列 / 占比 / TOP 域名） |
| `/app/logs` | QueryLog | 查询日志 |
| `/app/settings` | Settings | 账号设置 / 安全 / 导入导出 / 在线更新 |
| `/admin` | Admin | 管理后台（仅管理员） |
