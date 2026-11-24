# Track 3 · Pulse 风格托盘弹层、配置与主程序壳层（`feat/tray-shell`）

> 对应原方案 **Phase 3**，直连冻结的 `internal/model/` 与 `internal/mock/`，可与 Track 1 / Track 2 完全并行开发，并预留最终合流入口 `cmd/monitor/`。

## 1. 分支与工作区隔离（推荐 Worktree）

```powershell
git worktree add ..\antigravity_monitor_t3 feat/tray-shell
cd ..\antigravity_monitor_t3
```

## 2. 目录所有权与边界

- **独占可修改路径**：
  - `internal/config/`（新建：用户偏好 `model.Settings` 读写与原子保存）
  - `internal/ui/tray/`（新建：Pulse 风格毛玻璃托盘弹层、点阵图、系统托盘菜单）
  - `cmd/dev-tray/main.go`（独立托盘弹层调试入口，直连 `internal/mock`）
  - `cmd/monitor/main.go`（最终双形态合流入口，合流前支持 `-mock` 模式独立运行）
- **严禁修改路径（冻结契约 / 其他轨道独占）**：
  - `internal/model/`、`internal/mock/`、`internal/source/`、`internal/metrics/`、`internal/ui/theme/`、`internal/ui/anim/`、`internal/ui/desktop/`、`cmd/probe/`、`cmd/dev-desktop/`
  - *(注：由于 Track 2 与 Track 3 并行，Track 3 在 `internal/ui/tray/` 内先自包含所需的轻量色板/弹簧工具，合流阶段再统一复用 Track 2 的 `internal/ui/theme` 与 `internal/ui/desktop`)*

## 3. 核心交付目标（严格对齐 `docs/prototype.html` 托盘弹层与 Pulse 参考图）

使用最新版 **`github.com/egoist/mygo` (`v0.10.0`)** 的 `mygo.NewTray`、`FlyoutOptions`、`mygo/ui`、`ui.Canvas` 与 `plugins/glass` 构建极简托盘体验：

1. **极简配置持久化 (`internal/config/`)**：
   - 读写 `~/.config/antigravity-monitor/settings.json`（Windows 使用 `os.UserConfigDir()`）；
   - 持久化 `model.Settings`（开机驻留托盘、关闭退守托盘、主窗置顶、上下文 80% 预警、自动跟随活跃、主题模式、自定义定价路径）；
   - **明确不包含任何 TPS 模式切换项**（纯吐字速度已固定）。
2. **Pulse 风格托盘毛玻璃弹层 (`internal/ui/tray/`)**：
   - 通过 `mygo.NewTray` + `FlyoutOptions` 锚定系统托盘图标，点击平滑弹出/失焦收起；
   - **顶部 3 格弹簧分段胶囊**：`总览` / `性能` / `Agent 树`；
   - **右上角极简多对话切换器**：多对话并行时带呼吸点提示，点击展开毛玻璃下拉列表一键切换并行对话；
   - **双列等宽主指标 + Pulse 标志性实时点阵图（`ui.Canvas` / GPU 绘制）**：
     - 点阵图支持 60fps 平滑高度过渡；
     - 鼠标悬停任意一列，顶部微浮胶囊显示该次请求的 `#id · 命中% · TTFT · TPS`；
   - **中部进度条与底部精简列表**：
     - `总览`：上下文窗口条 + 缓存命中率 & TTFT（单次/总计）；
     - `性能`：缓存命中（单次 vs 总计）进度条 + 最近 3 次请求（`命中% · TTFT · TPS`）；
     - `Agent 树`：主 vs 子费用占比条 + 缩进节点明细（点击回调 `OnFocusAgent(convID, agentID)` 呼出并聚焦主窗口）；
   - **极简底栏**：左侧「打开主面板」按钮 + 右侧设置齿轮 Popover。
3. **独立调试入口 (`cmd/dev-tray/main.go`) 与合流入口 (`cmd/monitor/main.go`)**：
   - `cmd/dev-tray` 仅挂载系统托盘 + Flyout + `mock.New(2800 * time.Millisecond)`，方便快速调优托盘手感、位置与毛玻璃质感；
   - `cmd/monitor` 搭建干净的双窗生命周期框架（托盘常驻、关闭主窗退守托盘、右键退出、回调钩子 `DesktopLauncher`），合并 Track 1 & 2 后只需替换 5 行构造调用即可完成全功能发布。

## 4. 验收标准

- `go test ./internal/config/... ./internal/ui/tray/...` 与 `go build ./cmd/dev-tray ./cmd/monitor` 通过；
- 运行 `go run ./cmd/dev-tray` 可在 Windows 系统托盘出现图标并弹出毛玻璃 Flyout，3 个 Tab、点阵图悬停、多对话切换与设置持久化均可正常工作；
- 严格不修改冻结契约及其他轨道目录。

---

## 5. 可直接复制给新对话的启动 Prompt

```text
请先阅读仓库根目录的 README.md、docs/DESIGN.md、docs/prototype.html、internal/model/、internal/mock/ 以及 docs/tracks/TRACK_3_TRAY_SHELL.md。
请负责 Track 3（分支 feat/tray-shell），严格只在 Track 3 独占目录（internal/config/、internal/ui/tray/、cmd/dev-tray/、cmd/monitor/）内开发，不要修改 internal/model/、internal/mock/ 及其他轨道文件。
请先检查 github.com/egoist/mygo (v0.10.0) 的 NewTray / FlyoutOptions / ui / plugins/glass / ui.Canvas 实际 API，还原 docs/prototype.html 中 Pulse 风格的毛玻璃托盘弹层与配置持久化，并通过 go run ./cmd/dev-tray 与 go test 验证通过。
```
