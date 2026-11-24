# Track 2 · Apple Liquid Glass 桌面端原生 UI（`feat/desktop-ui`）

> 对应原方案 **Phase 2**，直连冻结的 `internal/model/` 与 `internal/mock/`，无需等待 Track 1 即可独立编译、运行并调优视觉和 60fps 动效。

## 1. 分支与工作区隔离（推荐 Worktree）

```powershell
git worktree add ..\antigravity_monitor_t2 feat/desktop-ui
cd ..\antigravity_monitor_t2
```

## 2. 目录所有权与边界

- **独占可修改路径**：
  - `internal/ui/theme/`（新建：Apple 浅/深色 Liquid Glass 配色、玻璃材质预设、单色 SF 线性矢量图标绘制）
  - `internal/ui/anim/`（新建：阻尼弹簧插值器、数值平滑滚动器、帧间曲线形变器）
  - `internal/ui/desktop/`（新建：完整桌面 APP 视图组件）
  - `cmd/dev-desktop/main.go`（独立桌面端调试入口，直连 `internal/mock`）
- **严禁修改路径（冻结契约 / 其他轨道独占）**：
  - `internal/model/`、`internal/mock/`、`internal/source/`、`internal/metrics/`、`internal/ui/tray/`、`internal/config/`、`cmd/probe/`、`cmd/dev-tray/`、`cmd/monitor/`

## 3. 核心交付目标（严格对齐 `docs/prototype.html` 视觉与动效）

使用最新版 **`github.com/egoist/mygo` (`v0.10.0`)** 的 `mygo/ui`、`plugins/glass`（Liquid Glass）与 `ui/chart` 构建极简 Apple 桌面端（纯 Go GPU 直绘，零 WebView）：

1. **主题与材质基座 (`internal/ui/theme/`)**：
   - 自动跟随/切换系统浅色与深色 Liquid Glass，遵循苹果中性灰阶排版 + 单一 Apple Blue 强调色（+ 克制 Amber 预警/次通道），**禁用彩色方块字标和花哨标签**；
   - 封装可复用的 Liquid Glass 卡片/内嵌侧栏/胶囊分段控件（复用 `glass.Glass{}`、`glass.Regular`、`Radius` 与 1px 顶部高光描边）；
   - 提供可被 Track 3 在合流后复用的导出接口（`theme.Palette`, `theme.GlassCard`, `theme.SegmentedPill`, `anim.Spring`）。
2. **60fps 物理动效层 (`internal/ui/anim/`)**：
   - 阻尼弹簧插值（数值滚动、滑块位移、曲线点位形变、悬停竖线平滑吸附）；
   - 无活跃动画且无新快照时暂停重绘，保证静息 GPU 接近 `0%`。
3. **完整桌面端视图 (`internal/ui/desktop/`)** — 导出 `desktop.New(provider model.SnapshotProvider, opts desktop.Options)` 及 `View(c *ui.Context)`：
   - **内嵌悬浮毛玻璃侧栏**：
     - 顶部极简会话切换器 + 多对话并行呼吸提示（`● N 进行中`，单对话时自动隐藏）+ 玻璃 Popover（含「自动跟随活跃」开关与手动点击锁定）；
     - **父子 Agent 可视化树**：合并项「整棵会话树」（单 Agent 会话时隐藏）+ 主 Agent + 缩进子节点 + 曲线连接线与活跃光点流。
   - **主画布（无歧义 Tree vs Single-Node 口径）**：
     - 标题区常驻单色 `合计 / 主 / 子` 三方费用对照；
     - **Hero 上下文窗口卡**：整棵树视图下右上角展示 `最高水位 xx.x% · 节点名` + 下方各节点并排可点击进度条；单节点视图下展示该节点自身大号百分比 + 单条进度条；
     - **4 张可点击极简指标卡**（费用 / 缓存命中 / 首字时长 / 纯吐字速度）：
       - 整棵树视图：缓存/TTFT/TPS 大字展示**全树加权汇总**，次行展示**最新单次 + 来源节点名**；
       - 单节点视图：大字展示**该节点最新单次**，次行展示**该节点总计/均值**；
     - **底部逐次请求卡（`曲线 | 逐次` 胶囊切换）**：
       - 曲线态：点击上方 4 卡平滑形变切换曲线，悬停弹簧吸附竖线，下方常驻「单次请求检视栏」（`‹ ›` 前后步进 + 完整 `命中% · TTFT · TPS · 费用 · Token`）；
       - 逐次态：留白式单次请求列表，点击行弹簧展开 Token 构成进度条。
4. **独立调试入口 (`cmd/dev-desktop/main.go`)**：
   - 启动 `mygo` 窗口（`TitleBarHidden` + `Vibrancy`），接入 `mock.New(2800 * time.Millisecond)`，支持快捷键或按钮触发 `TriggerBurst()` 直观检验实时动效。

## 4. 验收标准

- `go test ./internal/ui/anim/... ./internal/ui/desktop/...` 与 `go build ./cmd/dev-desktop` 通过；
- 运行 `go run ./cmd/dev-desktop` 可见真实毛玻璃窗口，界面干净克制、无花哨色块，交互与 `docs/prototype.html` 高度一致；
- 严格不修改冻结契约及其他轨道目录。

---

## 5. 可直接复制给新对话的启动 Prompt

```text
请先阅读仓库根目录的 README.md、docs/DESIGN.md、docs/prototype.html、internal/model/、internal/mock/ 以及 docs/tracks/TRACK_2_DESKTOP_UI.md。
请负责 Track 2（分支 feat/desktop-ui），严格只在 Track 2 独占目录（internal/ui/theme/、internal/ui/anim/、internal/ui/desktop/、cmd/dev-desktop/）内开发，不要修改 internal/model/、internal/mock/ 及其他轨道文件。
请先检查 github.com/egoist/mygo (v0.10.0) 的实际 API（ui、plugins/glass、ui/chart、WindowOptions），然后严格还原 docs/prototype.html 的 Apple Liquid Glass 极简视觉与 60fps 交互动效，通过 go run ./cmd/dev-desktop 验证运行与编译通过。
```
