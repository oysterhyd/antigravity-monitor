# Track 1 · 数据引擎与本地遥测解析（`feat/data-engine`）

> 对应原方案 **Phase 1**，完全独立于 UI，可与 Track 2 / Track 3 同步并行开发。

## 1. 分支与工作区隔离（推荐 Worktree）

```powershell
# 在主仓库外创建独立工作树，避免与其他并行对话冲突
git worktree add ..\antigravity_monitor_t1 feat/data-engine
cd ..\antigravity_monitor_t1
```

## 2. 目录所有权与边界

- **独占可修改路径**：
  - `internal/source/`（新建）
  - `internal/metrics/`（新建）
  - `assets/pricing.default.json`（如需补充新模型单价）
  - `cmd/probe/main.go`（替换为真实数据 CLI 验证工具）
- **严禁修改路径（冻结契约 / 其他轨道独占）**：
  - `internal/model/`、`internal/mock/`、`internal/ui/`、`internal/config/`、`cmd/dev-desktop/`、`cmd/dev-tray/`、`cmd/monitor/`

## 3. 核心交付目标

实现 `model.SnapshotProvider` 接口的真实本地只读数据引擎（例如 `source.NewProvider(opts)`），做到**零代理、零反代、零锁冲突、极低内存与 CPU**：

1. **L1 全局会话索引 (`internal/source/summaries_db.go`)**：
   - 使用纯 Go `modernc.org/sqlite` 以严格只读 WAL 模式打开 `~/.gemini/antigravity/conversation_summaries.db`（`?mode=ro&_query_only=1&_busy_timeout=150`）；
   - 读取 `conversation_summaries` 表：识别顶层对话（`parent_conversation_id == ""`）、按 `parent_conversation_id` 组装父子会话树、读取 `not_fully_idle` / `last_modified_time` / `last_user_input_step_index` / `agent_name` / `workspace_uris`。
2. **L2 高精度遥测解析 (`internal/source/conv_db.go` + `protobuf.go`)**：
   - 仅对活跃池（LRU 最多 5 个近期对话及其子 Agent）增量查询 `conversations/<id>.db` 的 `gen_metadata` 表（`WHERE idx > lastIdx`）；
   - 使用 `google.golang.org/protobuf/encoding/protowire` 轻量流式解码，跳过 `>4KB` 未知大载荷：
     - `f1.f4.f2` (`input_tokens`)、`f1.f4.f5` (`cache_read_tokens`)、`f1.f4.f4` (`cache_creation_tokens`，用本机 Claude 样本实测确认)、`f1.f4.f3` (`output_tokens` = `f9 + f10`)；
     - `f1.f9.f10.f1` / `f1.f9.f10.f4`（上下文占用 / 上限）；
     - `f1.f11`（`Duration` -> TTFT）、`f1.f12`（`Duration` -> 总耗时）；
     - `f1.f19` / `f1.f21` / `f1.f20`（模型规范名、展示名、`last_step_index`、`request_id`）。
3. **L3 明文日志对齐与 Subagent 三级命名 (`internal/source/transcript.go`)**：
   - 增量读取 `brain/<id>/.system_generated/logs/transcript.jsonl`：
     - 提取 `USER_INPUT` 确定精确 Turn 轮次分界（`InCurrentTurn`）；
     - 从父对话 `invoke_subagent` 工具调用中提取 `Role` 名称并与子 Agent `conversationId` 关联；
     - 实现三级命名兜底：`Role` -> `conversation_summaries.agent_name` -> 同名自动编号（`browser #1`, `browser #2`）；
     - 若 L2 Protobuf 缺字段，平滑降级读取 `PLANNER_RESPONSE` 中的明文 token。
4. **并发监听与指标定价 (`internal/source/watcher.go` + `internal/metrics/`)**：
   - `fsnotify` 监听 `conversations/` 与 `conversation_summaries.db-wal`，按 `conv_id` 独立 `150ms` 防抖；
   - 嵌入 `assets/pricing.default.json` 并支持用户外部 JSON 覆盖；
   - 调用 `model.ComputeDecodeTPS`（纯吐字速度）、`model.ComputeCacheHitRate`、`model.AggregateScope`、`model.ComputePeakContext` 产出完整 `model.AppSnapshot`。
5. **实测 CLI 工具 (`cmd/probe/main.go`)**：
   - 运行 `go run ./cmd/probe` 输出本机真实活跃/最近会话树、每个节点的上下文水位、本轮/会话费用、缓存命中率、TTFT、纯吐字 TPS 与最近 5 次请求明细；支持 `-watch` 实时流模式。

## 4. 验收标准

- `go test -race ./internal/source/... ./internal/metrics/...` 全部通过（含 Protobuf 真实样本单测、长上下文阶梯定价单测、多对话并行与父子树聚合单测）；
- `go run ./cmd/probe` 能无报错读取本机 `~/.gemini/antigravity` 真实数据，Token 与 `transcript.jsonl` 100% 对齐；
- 严格不修改 `internal/model/` 与其他轨道文件。

---

## 5. 可直接复制给新对话的启动 Prompt

```text
请先阅读仓库根目录的 README.md、docs/DESIGN.md、internal/model/ 以及 docs/tracks/TRACK_1_DATA_ENGINE.md。
请负责 Track 1（分支 feat/data-engine），严格只在 Track 1 独占目录（internal/source/、internal/metrics/、cmd/probe/、assets/pricing.default.json）内开发，不要修改 internal/model/、internal/mock/ 及其他轨道文件。
开始前请先切换到 feat/data-engine 分支（或提醒我使用独立 worktree），然后完整实现只读 SQLite WAL + protowire + transcript 三层数据引擎与 cmd/probe 验证工具，并用本机真实 Antigravity 数据跑通单测与精度核验。
```
