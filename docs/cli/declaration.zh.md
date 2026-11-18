# 代码与声明

[English](declaration.md) | 中文

读取代码、生成本 worktree 的清单，以及所有读取清单的命令共同遵守的边界。`git` 只读；`init` 只产出草稿，只有 `init adopt` 会写入 `oberth.yaml`。

## oberth git [path]

在 status 之外按需读取代码摘要、文件列表或 diff；`oberth git files --json` 不截断，`files_total` 是完整数量。`oberth git graph [path] --json` 返回完整提交拓扑、parent IDs、refs 与 worktree。`--limit N`（N > 0）仍可截断，并在 JSON 里把 `truncated` 标为 true、`limit` 标为 N；`--limit 0` 与省略该参数一样表示完整历史，此时 `limit` 为 0、`truncated` 为 false。Graph 明确标出 `observed_head`，仍保持只读，不 fetch、不刷新 index。`oberth git diff [path] --commit <hex> [--file <repo-relative-path>] --json` 审查指定 commit 相对第一个 parent 的 patch；root commit 相对空树，merge 不使用 combined diff。commit 只接受 7–64 位十六进制 object ID，响应带 `commit` 与 `base` 字段。

## oberth init

`init` 只做一件事：根据当前 worktree 的事实生成一份可审阅的 starter
`oberth.yaml`。监听端口和项目文件里的声明只作为注释，留给人决定。`--dry-run` 只预览，
`--json` 给机器读取。

~~~text
oberth init [--dry-run] [--json]
~~~

监听扫描失败不中止 `init`：项目自身的声明、可复制的条目形状都还在。只有一句结论不能写，
就是「当时没有服务在监听」。因此文件里的注释改成「监听未知」并带上观察错误码与下一步，
stderr 也说明一次。这与 `status` 的规则一致：看不见不是没在听。部分可读的扫描照常列出
读到的端口，注释说明这张表不完整。

### oberth init draft [agent] [--dry-run] [--json] [--progress]

让配置中的本机 agent 起草一份清单。option-berth 不判断、不联网，也不直接改
`oberth.yaml`：成功只产生一份草稿（默认归档在 `~/.option-berth/drafts/`），人读过后
再采纳。已有多个 agent 时，可用位置参数点名 `claude` 或 `codex`；不带参数时使用配置
或 PATH 上唯一可用的 agent。
`--progress` 输出 NDJSON 进度事件，适合客户端在 agent 工作期间显示当前阶段；最后一行是
`type: "done"` 的完整结果。

### oberth init adopt [draft|path] [--replace|--merge] [--dry-run] [--diff] [--json]

把人选中的草稿——或任意按路径指定的既有清单——写进当前 worktree 的 `oberth.yaml`。不带路径时
采用该项目最新草稿；带路径就是从另一份 checkout 继承声明的方式，之后两边各自独立。这一步才
会落盘。已有清单必须明确选择 `--replace` 或 `--merge`。`--dry-run` 只预览将要写入的完整清单；
再加 `--diff` 时，输出当前清单与采纳结果的只读 unified diff（JSON 输出放在 `diff` 字段），
不写清单也不移动草稿。

## 清单和状态的边界

- 清单由人写；agent 只能起草，`oberth init adopt` 才采纳。
- 清单属于 worktree 目录，不属于分支、也不属于仓库：引擎按文件系统查找它，从不问 git 它是否被
  跟踪。项目是否提交这份清单由项目自己决定；唯一的差别是：在已声明的 `sources:` 路径下，被
  跟踪的清单进入指纹，被忽略的不会。
- 新 checkout 一开始没有清单。`oberth init adopt <path>` 可以从任何既有 checkout 拷一份声明
  进来；之后两者各自独立。
- 当前文件名是 `oberth.yaml`。`.oberth.yaml` 和 `oberth.yml` 仍会被读取，不会再以它们新建，
  被遮蔽的或遗留的拼写由 `oberth doctor` 报告。
- option-berth 只读 git，不提交、不暂存、不切分支。
- machine: 是项目依赖的只读引用；up / down 不会替它起停。
- workspace: 是另一个 checkout 的服务地址引用；up / down / restart 不会替对方起停，也不建立
  depends_on 关系。
- `worktrees` 的账本只记「身份曾在哪个路径被看到」和最近一次组名；不记服务是否运行，
  也不自动删除 checkout 消失后的数据。
- 监听端口、pid、退出码和日志是 Run 的运行实况。产品不提供整机端口雷达、机器服务注册表、模型
  判断、MCP 服务或自动安装 skill。

### 清单中的引用

`prepare:`、`cmd:`、`env:` 的值和命令式 `health:` 可以引用端口和 checkout 身份，runtime service 在
启动服务前展开：

| 引用 | 展开为 |
|---|---|
| `${port}` / `${url}` | 本服务自己的端口 / `http://localhost:<port>` |
| `${<name>.port}` / `${<name>.url}` | 本清单的服务、`machine:` 引用或 `workspace:` 引用的端口 / URL |
| `${worktree}` | 当前 checkout 的稳定身份，形如 `wt_0123456789ab` |

`<name>` 先按本清单的 `services:` 查找；没有同名服务时再查 `machine:`（固定端口）和
`workspace:`（另一 checkout 的地址）。服务、machine 和 workspace 的名称因此不能互相重复。
命令式 `health:` 可以引用服务和 machine，但不能引用 workspace：扫描器在每次健康检查时不会去读
另一份 checkout 的地址。

`${worktree}` 由清单所在 checkout 的规范化根路径派生：同一个 worktree 每次启动都相同，
主 checkout 和每个 linked worktree 各不相同。它是标识符而不是路径，适合拼进数据库名
（`app_${worktree}`）或相对数据目录（`.data/${worktree}`）。移动 checkout 目录会得到新的
身份，旧数据不会被搬走。当前 checkout 的值直接读 `oberth status --json` 的
`worktree.identity`；本机见过哪些身份、哪些 checkout 已经不在了，用 `oberth worktrees`。
删除数据仍然由人完成，引擎只报告身份与路径的对应关系。

只有上表五种形状是引用；其他 `${...}`（例如 `${HOME}`、`${FOO:-x}`）原样保留。
`${worktree}` 不指向服务，不需要也不产生 `depends_on`；未声明的服务、没有端口的端口引用
仍按清单校验拒绝。可运行示例见 [`examples/stack-workshop`](../../examples/stack-workshop/README.md)：
它把 `${worktree}` 同时用于 SQLite 路径和每 worktree 一套的 Compose 项目；README 里能看到
`port: auto` 以 `PORT` 进入 Compose、`pg_isready` 健康检查，以及 `down` 让 Compose 自己停容器。

### 共享环境文件

服务可以声明 `env_file:`，指向一份 dotenv 风格的文件；相对路径按 `oberth.yaml` 所在目录解析，
所以 `../shared/dev.env` 可以放在 checkout 之外，让多个 worktree 共用同一份配置和密钥。
文件只在该服务启动或执行它的 `prepare:` 时读取，option-berth 不创建、不修改它。

空行和 `#` 开头的整行注释会被忽略；支持可选的 `export ` 前缀、单引号原样值和双引号
（转义 `\n`、`\r`、`\t`、`\\`、`\"`）。值不做变量插值，`${...}` 原样保留。写入环境的优先级
从低到高是：runtime service 自身环境、调用者环境、`env_file`、`PORT`、服务自己的 `env:`。文件缺失、
不是普通文件或某一行的格式错误会让该服务启动失败；错误只指出文件和行号，不回显值。文件内容
不进入 `status`、日志或 `spec_hash`，修改它不需要重启 runtime service，但已启动的服务要重启才会读到。

### 跨 worktree 的地址引用

`workspace:` 列出另一份 checkout 里的服务，只提供地址，不参与起停：

```yaml
workspace:
  - name: backend
    worktree: ../backend
    service: api
```

`worktree` 是另一 checkout 的根目录（相对本清单目录或绝对路径），也可以直接指向它的清单文件；
`service` 是那份清单里的服务名。之后 `prepare:`、`cmd:` 和 `env:` 可以用 `${backend.port}` /
`${backend.url}`。解析时读取对方清单和 runtime service 的运行实况：对方服务正在运行就用实际端口
（包括 `port: auto`）；不在运行但声明了固定端口就用声明的地址；`port: auto` 且没有运行则引用
无法解析，使用它的服务启动失败并说明原因。`up` / `down` / `restart` 不会启动或停止对方的服务，
也不建立 `depends_on` 关系。`workspace:` 名称不能与本清单的服务或 `machine:` 引用重名；
健康检查不接受 workspace 引用。

### 清单解析的歧义拒绝

`oberth.yaml` 只接受一个 YAML 文档；第二个文档（包括空文档）会报错，而不是被忽略。
同一服务不能重复声明 `port`，包括 `auto` 与数字混写；错误带 YAML 行号。
单文档、注释、正常数字端口和 `port: auto` 保持兼容。

### 清单 schema 与未知字段严格模式

清单的 JSON Schema 发布在 `engine/docs/schema/manifest.schema.json`，由 `internal/groups` 的声明
类型生成；`go generate ./...` 同时更新它与协议 schema。schema 拒绝未知键（顶层 `x-` 扩展与
`$schema` 键除外），端口只接受 1–65535 或 `auto`。`oberth init` 与 `oberth init draft` 写出的文件
首行带 `# yaml-language-server: $schema=...` modeline，编辑器据此在起草和采纳之前标出拼错的键；
手写清单可以自行加同一行。

默认解析仍忽略未知键以保持兼容。设 `BERTH_STRICT_MANIFEST=1`（也接受 `true`、`yes`、`on`）后，
未知键变成带行列位置的加载错误，覆盖 CLI、runtime service 和 `oberth init adopt` 的所有清单读取路径；
顶层 `x-` 与 `$schema` 仍然是合法元数据。

`doctor --only project_config` 在默认模式下以 warn 报告被忽略的清单键及行列位置，不回显字段值；
`env` 自定义键与顶层 `x-` 扩展不被误报。不改写清单、不执行命令，也不为补充报告元数据连接 runtime service。
