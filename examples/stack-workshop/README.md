# 复杂一点的整栈 / The Stack Workshop

`hello-worktree` 证明一条链路，`worktree-data` 证明每份 checkout 的数据隔离。
这个示例回答另一个问题：**当项目不再是一个脚本，而是一整栈互相依赖、启动有先有后、
有的服务根本没有端口时，人和 agent 还能不能只靠一份事实把整栈跑起来、看明白。**

它只需要已安装的 `oberth` 和 Python 3；没有 npm、容器、模型账号或 API key。
所有 HTTP 服务只监听 `127.0.0.1`。

## 这份清单里有什么

十个服务覆盖了大项目最常见的几类麻烦：

| 服务 | 形态 | 它让示例证明什么 |
|---|---|---|
| `auth` | HTTP，启动前预热 4 秒 | 慢启动不是失败；依赖要等它真正就绪 |
| `catalog` | HTTP | 普通依赖 |
| `api` | HTTP，`prepare:` 跑数据库迁移，依赖 `auth` + `catalog` | 前置命令、迁移、按服务等待依赖 |
| `gateway` | HTTP，依赖 `api` | 多级依赖，只有上游健康才对外健康 |
| `web` | HTTP，`prepare:` 构建 `dist/`，输入是 `src/app.html` 和 `variant.txt` | 生成物不在 Git 里；输入变了要重新构建 |
| `search` | HTTP，依赖 `catalog` | 与 `api` 平行的另一条依赖链 |
| `billing` | HTTP | 独立服务，不该被别的服务带停 |
| `ingest` | HTTP，依赖 `catalog` | 第二个消费者 |
| `worker` | 无端口，轮询 `api` | 没有端口不等于没有运行 |
| `notifier` | 无端口，轮询 `api` | 多个无端口进程各自归属 |

`machine:` 里的 `postgres` 和 `redis` 是**只读引用**：`status` 只说它们在不在听，
`up` / `down` 不会碰它们。示例本身不依赖它们启动，因此没有数据库或容器也能跑通。

## 从仓库运行

```sh
cd examples/stack-workshop
oberth up --wait --json
oberth status --json
oberth logs worker --once
```

`auth` 会先打印 `warming up`，几秒后才开始监听；`up --wait` 会等到依赖和健康检查都就绪
才返回。`worker` 和 `notifier` 没有端口，`status` 仍会列出它们，并在启动后写入
`.berth/<worktree>/worker.heartbeat` 与 `notifier.heartbeat`。

读 `status` 时注意三件事：

- 每个带端口的服务都有自己的实际端口；`port: auto` 不需要人分配。
- `worker` / `notifier` 显示为运行中，但没有 `listening on`。
- `machine:` 两个引用是 `listening` 或 `not listening`，与项目服务的行分开。

结束：

```sh
oberth down --force --json
```

`down` 只停这份清单所属 worktree 的服务，不删除 `dist/`、`.berth/` 或别处的东西。

## 只起一个子栈

大项目里经常只需要一条链路。`up --only` 只启动**点名的服务**；它们声明的依赖会被等待，
但不会自动加入启动集合。所以要起 API 这条链路，要同时点名它依赖的服务：

```sh
oberth up --only api,auth,catalog --wait --json
```

这会起 `auth`、`catalog` 和 `api`，不会起 `gateway`、`web`、`worker` 或 `notifier`。
再用 `oberth status` 确认没被点名的服务仍然停着。

## 改完代码，跑的是不是新代码

修改 `variant.txt` 或 `src/app.html` 后，再执行一次：

```sh
oberth restart --only api,web --json
oberth up --wait --json
```

`restart` 会重跑受影响服务的 `prepare:`；`web` 的 `prepare_inputs` 包含
`src/app.html` 和 `variant.txt`，所以构建会重跑，`dist/version.txt` 变成新值。
`status` 里 `manifest_runtime_mismatch` 只回答"进程是否符合当前清单"，
不回答"进程是不是用刚改的源码启动的"——后者要靠重启和输出复核。

## 看见两个 worktree 各自独立

在两个由你创建的 Git worktree 里分别进入本示例，各执行一次 `oberth up --wait --json`：

- 两边所有自动端口都不同；
- `.berth/<worktree>/app.db` 是两份文件，`worker.heartbeat` 也是两份；
- 只对一边执行 `down`，另一边的服务和无端口 worker 继续运行。

`oberth` 不创建、修改或删除 Git worktree；这些操作仍由你或现有 Git 工具负责。

## 可重复验收

从仓库根目录：

```sh
mage buildEngine
python3 scripts/verify-example.py --binary "$PWD/bin/oberth"
```

验收脚本在一个一次性 worktree 中复制本示例、启动整栈、检查健康与无端口 worker，
结束后回收自己创建的进程，不接触你日常的 daemon、账本或仓库。

## 常见问题

- `auth` 还没就绪：这是示例故意的慢启动；`up --wait` 会等，直接 `curl` 太早会得到 503。
- `web` 起不来：先看 `dist/` 是否生成；`prepare:` 的失败会保留在 `oberth logs web --once`。
- 迁移失败：`oberth logs api --once` 保留真实错误；不要用超时当作成功。
- 项目在 HOME 外：把示例放在 HOME 下，或明确审阅后使用 `--allow-outside-home`。
