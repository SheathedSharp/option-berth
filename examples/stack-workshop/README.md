# 一整栈 / The Stack Workshop

`hello-worktree` 证明一条链路能跑通。这个示例回答另一个问题：**当项目不再是一个脚本，
而是一整栈互相依赖、启动有先有后、有的服务没有端口、数据库还跑在容器里时，
人和 agent 还能不能只靠一份事实把整栈跑起来、看明白。**

它需要已安装的 `oberth`、Python 3，以及 **Docker**（`db` 是一个真实的 Postgres 容器）。
没有 npm、pip、模型账号或 API key；所有 HTTP 服务只监听 `127.0.0.1`。

## 这份清单里有什么

十个 Python 服务加一个容器，覆盖大项目最常见的几类麻烦：

| 服务 | 形态 | 它让示例证明什么 |
|---|---|---|
| `auth` | HTTP，启动前预热 4 秒 | 慢启动不是失败；依赖要等它真正就绪 |
| `catalog` | HTTP | 普通依赖 |
| `db` | Docker Compose 里的 Postgres，`port: auto` | 端口注入容器、容器内健康检查、`down` 让 Compose 自己停容器 |
| `api` | HTTP，`prepare:` 迁移本 worktree 的 SQLite，依赖 `auth` + `catalog` + `db` | 前置命令、迁移、HTTP 与 TCP 两类依赖 |
| `gateway` | HTTP，依赖 `api` | 多级依赖，只有上游健康才对外健康 |
| `web` | HTTP，`prepare:` 构建 `dist/`，输入是 `src/app.html` 和 `variant.txt` | 生成物不在 Git 里；输入变了要重新构建 |
| `search` | HTTP，依赖 `catalog` | 与 `api` 平行的另一条依赖链 |
| `billing` | HTTP | 独立服务，不该被别的服务带停 |
| `ingest` | HTTP，依赖 `catalog` | 第二个消费者 |
| `worker` | 无端口，轮询 `api` | 没有端口不等于没有运行 |
| `notifier` | 无端口，轮询 `api` | 多个无端口进程各自归属 |

`machine:` 里只剩 `redis`，它是**只读引用**：`status` 只说它在不在听，`up` / `down`
不会碰它。Postgres 不再写在 `machine:` 里，它是上面由本 worktree 拥有并启动的 `db` 服务。

## 从仓库运行

先确认 Docker 在跑（`docker compose version` 能返回版本），然后：

```sh
cd examples/stack-workshop
oberth up --wait --json
oberth status --json
oberth logs worker --once
```

`auth` 会先打印 `warming up`，几秒后才开始监听；`db` 会先拉取镜像再启动容器。
`up --wait` 会等到依赖和健康检查都就绪才返回。`worker` 和 `notifier` 没有端口，
`status` 仍会列出它们，并在启动后写入 `.berth/<worktree>/worker.heartbeat`
与 `notifier.heartbeat`。

读 `status` 时注意四件事：

- 每个带端口的服务都有自己的实际端口，包括 `db`；`port: auto` 不需要人分配。
- `api` 的 `/health` 只有在 `auth`、`catalog` 健康、`db` 的端口可连时才返回 200；
  同一个响应里还带着本 worktree SQLite 的 `db`、`migrations`、`notes`。
- `worker` / `notifier` 显示为运行中，但没有 `listening on`。
- `machine:` 的 `redis` 是 `listening` 或 `not listening`，与项目服务的行分开。

结束：

```sh
oberth down --json
```

**这个示例要用普通 `down`，不要用 `down --force`。** 普通 `down` 发 SIGTERM，
`docker compose` 收到后自己停止容器；`--force` 直接 SIGKILL，Compose 没有机会收尾，
容器可能留在机器上。普通 `down` 等 5 秒后才升级信号，所以 `docker-compose.yml` 把
容器的 `stop_grace_period` 设成 2 秒，保证停容器落在 oberth 的窗口内。

`down` 只停这份清单所属 worktree 的服务，不删除 `dist/`、`.berth/`、容器卷或别处的东西。
要回收某个 worktree 的容器数据，由你在该 worktree 内运行
`docker compose -p app_<identity> down --volumes`；`identity` 来自 `oberth status --json`。

## 容器是怎么接进来的

`docker-compose.yml` 里有四个相互配合的事实：

| 事实 | 为什么 |
|---|---|
| `cmd: docker compose ... up` | 前台 Compose 会收到 `oberth down` 的 SIGTERM，并停止自己的容器。 |
| `-p app_${worktree}` | 容器、网络和卷都按 checkout 分开。 |
| `port: auto` + Compose 里的 `"${PORT}:5432"` | option-berth 分配端口并以 `PORT` 传入，Compose 负责映射。 |
| `health: command: ... exec -T db pg_isready` | 健康在容器里检查，不依赖宿主机是否装了 `pg_isready`。 |

如果数据库是**一套供所有 worktree 共用**的，不要把它声明成受管服务：任一 worktree 的
`down` 都会停掉别人正在用的数据库。那种写法是用 `machine:` 只读引用，由你在
option-berth 之外启动它。本示例选择了每 worktree 一套，所以 `db` 是受管服务。

## 每个 worktree 一份数据

`${worktree}` 展开成当前 checkout 的稳定身份，形如 `wt_0123456789ab`：由 checkout 的
规范化根路径派生，同一个 worktree 每次启动都相同，两个 worktree 一定不同。示例把它
同时放进数据目录、库名和 Compose 项目名：

```yaml
env:
  DB_PATH: .berth/${worktree}/app.db   # api 的 SQLite
  DB_NAME: app_${worktree}             # Postgres 容器的库名（Compose 读取）
```

`${worktree}` 是标识符，不是路径：把它嵌进相对路径或数据库名。它只在 `prepare`、`cmd`
和 `env` 里展开；其它 `${...}`（例如 `${HOME}`）仍按原样交给命令。它由 checkout 的路径
派生，所以移动 worktree 目录会得到新身份，旧数据不会被自动搬走。

`api` 的 `prepare:` 在每次启动前执行 `migrate.py`，建表并写入一条种子记录；迁移是幂等的，
所以 `restart` 重跑也不会产生第二份数据。`api` 的 `/health` 返回实际数据库路径、迁移数和
记录数。

## 只起一个子栈

大项目里经常只需要一条链路。`up --only` 会启动点名的服务及其声明的依赖闭包：

```sh
oberth up --only api --wait --json
```

这会起 `auth`、`catalog`、`db` 和 `api`，不会起 `gateway`、`web`、`worker` 或 `notifier`。
只想起点名的那一个、不展开依赖时加 `--no-deps`，但那样 `api` 会因为没有上游而保持 503。
再用 `oberth status` 确认没被点名的服务仍然停着。

## 改完代码，跑的是不是新代码

修改 `variant.txt` 或 `src/app.html` 后，再执行一次：

```sh
oberth restart --only api,web --json
oberth up --wait --json
```

`restart` 会重跑受影响服务的 `prepare:`；`web` 的 `prepare_inputs` 包含
`src/app.html` 和 `variant.txt`，所以构建会重跑，`dist/version.txt` 变成新值。
`status` 里 `manifest_runtime_mismatch` 只回答“进程是否符合当前清单”，
不回答“进程是不是用刚改的源码启动的”——后者要靠重启和输出复核。

## 看见两个 worktree 各自独立

在两个由你创建的 Git worktree 里分别进入本示例，各执行一次 `oberth up --wait --json`：

- 两边所有自动端口都不同，包括 `db` 的宿主机端口；
- 两边的 Compose 项目是 `app_wt_…`，容器、网络和卷各一份；
- `.berth/<worktree>/app.db` 是两份文件，`worker.heartbeat` 也是两份；
- 只对一边执行普通 `down`，另一边的服务、容器和无端口 worker 继续运行。

`oberth` 不创建、修改或删除 Git worktree；这些操作仍由你或现有 Git 工具负责。

## 怎么验证

每 PR 的冒烟只跑最小示例 `hello-worktree`，不跑这里，也不碰 Docker。这个复杂示例是
**手动基准**的输入：基准在 Linux、需要 Docker，比较装了 oberth 与不装 oberth 的 agent
在同一个整栈上的完成情况，不是合并门禁。怎么发起、看什么数，见
[`docs/agent/agent-eval.md`](../../docs/agent/agent-eval.md)。

手工跑通本示例仍然用上面的 `oberth up --wait --json` / `status --json` / 普通 `down --json`；
验收脚本不负责它，避免把 Docker 和 token 成本带进每 PR 的门禁。

## 常见问题

- `db` 起不来：先确认 Docker daemon 在跑、`docker compose version` 可用；容器内的
  `pg_isready` 才是健康依据，宿主机不需要装 Postgres 客户端。
- `auth` 还没就绪：这是示例故意的慢启动；`up --wait` 会等，直接 `curl` 太早会得到 503。
- `web` 起不来：先看 `dist/` 是否生成；`prepare:` 的失败会保留在 `oberth logs web --once`。
- 迁移失败：`oberth logs api --once` 保留真实错误；不要用超时当作成功。
- 端口被残留容器占着：用 `docker compose -p app_<identity> down` 回收，先别杀别的进程。
- 项目在 HOME 外：把示例放在 HOME 下，或明确审阅后使用 `--allow-outside-home`。
