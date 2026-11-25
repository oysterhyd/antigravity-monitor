# Antigravity Monitor

轻量级、零代理、零 WebView 的 **Antigravity 上下文与 Token 实时统计原生桌面面板**。

基于 **Go + 最新版 [`egoist/mygo`](https://github.com/egoist/mygo) (`v0.10.0`) Native UI（Direct3D 11 / Metal / OpenGL GPU 直绘）+ `plugins/glass`（Apple Liquid Glass）** 构建，直接只读解析 `~/.gemini/antigravity` 本地 SQLite WAL 与 Protobuf 记录。

## 核心特性

- **双形态原生体验**：Pulse 风格系统托盘毛玻璃弹层（Tray Flyout）+ Apple Liquid Glass 极简桌面主窗口
- **零代理本地高精度读取**：只读增量解析 `conversation_summaries.db`、`conversations/<id>.db`（`gen_metadata` Protobuf）与 `transcript.jsonl`
- **全维度实时指标**：上下文窗口水位、本轮/会话费用、缓存命中率、纳秒级首字时长（TTFT）、**纯吐字速度（Pure Decode TPS）**——每项均支持单次请求回看与加权汇总
- **父子 Agent 可视化拓扑**：同时清晰区分「整棵会话树汇总」「主 Agent 独立」与「各 Subagent 节点」，支持多对话并行自动跟随与手动锁定
- **账号额度**：自动读取 Gemini 和 Claude & GPT 的五小时及周额度。Windows 会复用本机可用的额度读取组件，找不到时在首次联网启动后从 Google 官方下载到应用缓存。额度读取需要可用的 Antigravity 登录会话。应用启动时读取，此后每 30 秒刷新。趋势图每五分钟保留一个采样点，覆盖最近七天。升级前的手动校准与估算趋势不会导入新历史。

## 快速运行与构建

```powershell
# 直接运行（默认只读监测本机 ~/.gemini/antigravity 实时会话）
go run ./cmd/monitor

# 离线体验仿真数据（内置 2 个并行对话 + 1 个空闲对话）
go run ./cmd/monitor -mock

# 仅驻留系统托盘启动
go run ./cmd/monitor -tray-only

# CLI 快速校验本机真实数据解析
go run ./cmd/probe

# 编译 Windows 无黑框单文件原生应用
go build -ldflags="-H=windowsgui" -o antigravity-monitor.exe ./cmd/monitor
```

## 文档与视觉定稿

- 终版设计方案：[docs/DESIGN.md](docs/DESIGN.md)
- 已定稿交互原型（浏览器直接打开）：[docs/prototype.html](docs/prototype.html)

## 三线并行开发指南

本仓库已冻结共享契约（`internal/model/`）与仿真数据源（`internal/mock/`），三条轨道拥有完全独立的目录与分支，可在 3 个并行对话中互不阻塞地同时开发（推荐配合 `git worktree` 各自使用独立工作目录）：

| 轨道 | 分支名 | 独占目录 | 独立验证命令 | 任务书 |
| :--- | :--- | :--- | :--- | :--- |
| **Track 1 · 数据引擎** | `feat/data-engine` | `internal/source/`, `internal/metrics/`, `cmd/probe/` | `go run ./cmd/probe` | [TRACK_1_DATA_ENGINE.md](docs/tracks/TRACK_1_DATA_ENGINE.md) |
| **Track 2 · 桌面端 UI** | `feat/desktop-ui` | `internal/ui/theme/`, `internal/ui/anim/`, `internal/ui/desktop/`, `cmd/dev-desktop/` | `go run ./cmd/dev-desktop` | [TRACK_2_DESKTOP_UI.md](docs/tracks/TRACK_2_DESKTOP_UI.md) |
| **Track 3 · 托盘与壳层** | `feat/tray-shell` | `internal/ui/tray/`, `internal/config/`, `cmd/dev-tray/`, `cmd/monitor/` | `go run ./cmd/dev-tray` | [TRACK_3_TRAY_SHELL.md](docs/tracks/TRACK_3_TRAY_SHELL.md) |

> **协作铁律**：并行开发期间，三条轨道均不要修改 `internal/model/` 与 `internal/mock/`，保证合流时零冲突。
