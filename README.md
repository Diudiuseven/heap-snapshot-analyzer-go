# heap-snapshot-analyzer-go

本仓库提供一套**完全在本进程内运行**的堆内存快照分析与泄漏归因能力：
从本地快照文件或进程内快照流出发，构建分配点与对象引用关系，完成聚合估计、
多快照对比归因、保留大小计算与时间序列泄漏判定。相同输入在任意处理顺序与
并发度下产出完全一致的结果。

## 构建与测试

```sh
go build ./...
go test ./...          # 单元 + 端到端测试
go test ./pkg/store/ -race
```

CLI：

```sh
go build -o heap-analyzer ./cmd/heap-analyzer
heap-analyzer import -store st snap0.jsonl snap1.jsonl
heap-analyzer top    -store st -snap s0
heap-analyzer diff   -store st -old s0 -new s1
heap-analyzer leaks  -store st -min-bytes 1048576 -min-ratio 0.2 -window 8
heap-analyzer retain -store st -snap s1
```

## 数据模型

| 概念 | 类型 | 说明 |
|---|---|---|
| 对象 | `model.Object` | `ID, Size, Type, Site, ScaleNum, ScaleDen` |
| 引用 | `model.Reference` | 有向边 `From -> To` |
| 分配点 | `model.AllocSite` | 一组栈帧；`SiteID 0` 保留为「未知」 |
| 类型 | `model.TypeInfo` | `TypeID 0` 保留为「未知」 |
| 快照 | `model.Snapshot` | 一次堆的完整记录 |

### 快照流格式（JSONL，每行一条记录）

```json
{"kind":"meta",   "id":"s1", "timestamp":1696000000, "source":"proc/1"}
{"kind":"site",   "numID":1, "frames":[{"func":"main.alloc","file":"a.go","line":10}]}
{"kind":"type",   "numID":1, "name":"*bytes.Buffer"}
{"kind":"object", "numID":7, "size":64, "type":1, "site":1, "scaleNum":4, "scaleDen":1}
{"kind":"ref",    "from":7, "to":8}
```

记录可乱序出现；读取端（`pkg/parse`）是流式的，内存中只驻留一条记录。

## 采样估计与确定性（`pkg/estimate`）

- 带采样记录携带 `ScaleNum/ScaleDen`（采样概率 p = ScaleDen/ScaleNum）。
  使用 Horvitz–Thompson 估计量：每条记录按 1/p 放大，对总体总量无偏。
- **确定性契约**：每条记录的估计只用整数运算（round-half-away-from-zero），
  聚合是 int64 求和。整数加法满足交换律/结合律，因此同一输入在任意处理
  顺序、任意并发度下估计结果逐位一致（`pkg/aggregate` 的 `Merge` 同样如此）。
- `ScaleDen <= 0` 视为未采样（放大系数 1）。

## 快照对比归因（`pkg/diff`）

两个快照按 (site, type) 桶对比，每条记录被分类为：

- `new`：仅出现在新快照；
- `collected`：仅出现在旧快照（已被回收）；
- `growing` / `shrinking` / `stable`：两侧都有，按字节差分类。

**无法归属的字节**（分配点未知 = 符号缺失或采样缺口，`SiteID == 0`）单独计入
`UnattributedOld/UnattributedNew`，**绝不并入任何已归属条目**。

## 保留大小（`pkg/retain`）

- 用 Lengauer–Tarjan 算法构建支配树；节点保留大小 = 自身估计大小 + 支配子树之和。
- 每个节点只出现在一棵支配子树中，因此**共享引用不会被重复计算**（共享节点
  只被虚拟根支配，不归入任何一个引用者）。
- **引用环**由支配算法天然处理（环归入其入口点）；无入边的闭环节点被提升为
  合成根，保证算法必终止且字节不丢失。

## 泄漏判定（`pkg/leak`）

配置项（`leak.Config`）：

| 配置 | 含义 | 默认建议 |
|---|---|---|
| `Window` | 只考察最近 N 个观测点（0 = 全部） | 5–10 |
| `MinGrowthBytes` | 窗口内绝对增长下限 | 1 MiB |
| `MinGrowthRatio` | 窗口内相对增长下限（0 关闭） | 0.2 |
| `MinConsecutiveIncreases` | 窗口末尾最少连续上升次数 | 2 |

**误报与漏报的处理策略（可解释）**：

- 绝对阈值与相对阈值**同时**满足才标记：避免小桶的比例噪声（误报来源 A）
  与大桶的正常绝对增长（误报来源 B）。
- `MinConsecutiveIncreases` 提供滞回：单次 GC 延迟造成的尖峰不会触发
  （误报来源 C）。代价是：窗口中段出现回落、之后又恢复增长的泄漏会**延迟
  一个窗口**才被标记（已知漏报来源，见下）。
- `Window` 限制历史长度：窗口之外的历史增长不会触发告警；相应地，增长
  完全发生在窗口之外、窗口内持平的真实泄漏会被漏报。需要更长记忆时调大
  `Window`。
- 从 0 增长的桶：相对增长用绝对字节数代替比例参与比较，避免除零导致的
  漏报。

## 增量导入与一致性语义（`pkg/store`）

- 导入先写入 `tmp/<id>/` 暂存目录，完成后通过**单次原子 rename** 提交到
  `snapshots/<id>/`，再在写锁下切换内存索引。
- 任一查询（`Store.Query`）拿到的是索引的**一致性快照**：要么看到导入前、
  要么看到导入后的状态，永远不会看到半写入的索引。
- 导入失败或被 `context` 取消：暂存目录被清理，存储保持逐字节不变。
- 进程崩溃后重新 `Open`：遗留的暂存目录与不完整的快照目录被确定性清理。
- 内存中的索引只保存每个快照的**聚合表**（大小正比于桶数而非对象数）；
  原始记录流保留在磁盘上，按需重读。

## 大快照与内存上界（`pkg/spill`）

- 导入与聚合路径完全流式：峰值内存正比于**分配点 × 类型**的桶数，
  不随对象数增长（端到端测试用 200 万对象、约 150MB 的流验证堆增长
  远低于输入规模）。
- `pkg/spill.Int64Array` 提供磁盘支撑数组（固定大小记录 + 有界写穿缓存），
  供需要在超大规模图上做逐节点计算的场景使用。
- `pkg/retain` 的支配树计算当前为内存实现，适用于常规规模或对引用图
  降采样后的场景；超大规模图请走流式聚合 + diff + leak 主链路。

## 符号缺失与多来源合并（`pkg/symbol`）

- 多个符号表用 `Merge` 合并：先出现的表优先，后出现的表只补空缺。
- 无法解析的 PC 生成占位帧 `<missing:0x...>`，**以可区分形式保留**在结果里，
  绝不静默丢弃；占位帧携带原始 PC，可定位回原始快照记录。
- 分配点完全缺失的对象进入 `SiteID 0` 桶，在 diff 中体现为
  `Unattributed*`，与已归属数据严格分离。

## 测试覆盖

- `pkg/estimate`：无偏性（蒙特卡洛均值）、取整、**顺序无关性**（随机打乱 20 次结果一致）。
- `pkg/retain`：共享引用不重复计数、引用环终止、闭环提升为根、自环。
- `pkg/diff`：new/collected/growing/shrinking/stable 分类、unattributed 隔离、条目顺序确定。
- `pkg/leak`：持续增长标记、尖峰/绝对阈值/比例阈值/窗口/乱序输入/输出顺序。
- `pkg/store`：失败导入无痕迹、取消导入无痕迹、崩溃恢复清理、并发导入+查询（-race）、重复 ID 拒绝。
- `pkg/spill`：缓存淘汰后数据正确。
- `tests/`：200 万对象流式导入的内存上界、导入→diff→leak 全链路归因。
