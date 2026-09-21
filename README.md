# 页脉探针（Page-Vein Probe）

检查 PDF 的**增量修订历史**，而不是只把文件读成“最后一版页面”。系统导入时按字节
保存原件，从尾部 `startxref` 追踪每一段 xref table / xref stream、trailer、object
stream 与 `/Prev` 链，建立对象在各修订中的版本图。**绝不自动重写或修复原文件。**

## 运行

```bash
go build ./...
go test ./... -count=1
go run ./cmd/server --addr 127.0.0.1:5246
# 打开 http://127.0.0.1:5246 ，页面标题含「页脉探针」
```

- 原件字节：`data/originals/<docid>.pdf`（逐字节保存，永不改写）
- 元数据库：`data/pagevein.db`（SQLite，纯 Go 驱动 modernc.org/sqlite，无 CGO、无外部 PDF 程序）
- 导出包：点击右上角「导出审阅清单与对象字节」下载 zip

## 浏览器能力

- **修订时间轴**：旧→新排列每个 revision 的 xref 类型、偏移、字节段落、条目数、`/Prev`、`/Root`
- **对象版本图**：对象按 编号/代次 分组，区分 `按声明找到` / `启发式找到` / `对象流内` / `free`
- **对象详情**：原始字节范围、解析值（字典/流 filter/长度/预览）、所属修订、被哪些对象引用
- **对象关系图**：引用方→当前对象的 SVG 图
- **原始十六进制**：逐字节 offset/hex/ascii；压缩对象先展示宿主编码字节，再展示展开内容
- **损坏诊断**：偏移越界/指向空白、重复对象号、`/Prev` 断开或成环、混合 table/stream 等
- **恢复候选与分支**：两个候选竞争同一对象版本时不按最晚偏移自动决定；人工确认后形成
  解释分支，旧分支仍可复现（重启后确认与分支保持一致）

## 关键安全边界

- `按声明`（xref table/stream、ObjStm 索引）与 `启发式`（原始字节扫描）候选严格分开，
  每个候选都带证据（来源、偏移、头部是否吻合、核验结论）。
- 流只在 **长度 / filter / 边界（EOL+endstream）全部核验**后才展开；仅支持 `/FlateDecode`，
  遇到不支持的 filter 或展开超过 `MaxExpandedBytes`（默认 64 MiB）时安全停止并报诊断。
- `/Length` 为间接引用时解析对应对象，解析不到则拒绝猜测。
- 导出包中的 `original.pdf` 与导入字节逐字节一致，另附每个版本的字节切片与 hex；
  **不会**用重新保存/修复的 PDF 充当证据。

## 代码结构

```
cmd/server        HTTP 服务入口
cmd/mkfixture     生成小型测试 PDF（内置于 internal/pdf 的夹具构造器）
internal/pdf      字节级扫描器：xref 链、字典/对象解析、ObjStm、flate、启发式、引用图
internal/store    SQLite：documents / decisions / branches
internal/app      导入扫描、人工确认、解释分支、字节取证、审阅 zip 导出
internal/web      单页 UI 与 JSON API（embed 静态资源）
```

## 测试夹具覆盖

`go test ./...` 用代码构造的小型 PDF 覆盖：增量更新、free entry 代次、xref stream、
object stream、错误偏移（指向空白/越界）、previous 环、重复候选竞争、尾随未引用数据、
解压上限、混合 table/stream，以及重启后人工确认与分支的一致性。
