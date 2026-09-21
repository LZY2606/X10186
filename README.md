# 页脉探针（yemai）

检查 PDF 的**增量修订历史**，而不是只读取最后一版页面。导入时按字节保存原件，
从尾部 `startxref` 追踪每一段 xref table / xref stream、trailer、object stream 与
`/Prev` 链，建立对象在各修订中的版本图。系统**绝不自动重写或修复原文件**。

## 构建与运行

```bash
go build ./...
go test ./... -count=1
go run ./cmd/server --addr 127.0.0.1:5246
# 打开 http://127.0.0.1:5246 ，页面标题为「页脉探针」
```

数据保存在项目目录 `./data`：SQLite (`yemai.db`)、`originals/`（逐字节原件）、
`objects/`。不调用任何外部 PDF 程序；SQLite 驱动为 CGO 的 `mattn/go-sqlite3`
（本机需有 C 编译器，macOS 自带 clang）。

## 取证原则

- **按声明 vs 启发式严格分开**：xref table / xref stream / object stream 里声明的候选标记为
  `declared`；在未覆盖字节上扫描 `N G obj` 得到的标记为 `heuristic`，二者永不静默合并。
- **竞争候选不按偏移裁决**：同一 (修订, 对象号, 代次) 出现多个候选时进入「竞争候选」，
  必须人工确认。确认后形成解释分支；从旧分支 `fork` 时复制决定，旧分支永远可复现。
- **压缩对象仅在核验后展开**：`/Length`、`/Filter`、`stream…endstream` 边界全部核验；
  仅展开 `FlateDecode`，其它过滤器保持不展开；单流展开超过限额（默认 64 MiB）安全停止并诊断。
- **证据不替换**：导出 ZIP 内含逐字节 `original.pdf`、按偏移切出的对象字节、仅在核验后展开的
  ObjStm 成员、`report.json`、`manifest.json` 与中文《审阅清单》。没有重新保存的 PDF 充当证据。

## 可识别的损坏形态

偏移指向空白/越界、重复对象号、断开的 `/Prev`、previous 环、table 与 stream 混用、
游离 `startxref`、最后 `%%EOF` 之后的尾随数据、未声明字节间隙、错误类型行、
ObjStm 索引/编号越界、解压炸弹超限。

## 代码结构

- `internal/forensic`：词法/语法解析、xref table 与 xref stream、ObjStm 展开、
  startxref/Prev 链、候选/版本图/诊断、受限 Flate 解压。
- `internal/store`：SQLite 持久化（documents / branches / decisions）与原件落盘。
- `cmd/server`：HTTP API 与内嵌单页前端（时间轴、对象表、关系图、十六进制片段、
  诊断、恢复候选与分支、ZIP 导出）。

## 测试夹具

`internal/forensic/scanner_test.go` 与 `internal/store/store_test.go` 构造小型 PDF 覆盖：
增量更新、free entry 代次、xref stream、object stream、错误偏移、previous 环、
重复候选、尾随数据、解压上限、table/stream 混用；存储测试验证重启后人工确认与分支一致。
