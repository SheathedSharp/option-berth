# 每个 worktree 一个数据库 / One database per worktree

这份示例演示 `oberth.yaml` 里的 `${worktree}` 引用：同一个仓库的两个 worktree
同时 `up` 时，各自迁移、各自读写自己的数据库，互不影响；只停一边也不会碰另一边。

数据库用 Python 标准库自带的 SQLite，因此和 `hello-worktree` 一样只需要已安装的
`oberth` 与 Python 3：没有容器、数据库服务、npm/pip 依赖或账号。

## 它怎么隔离

`${worktree}` 展开成当前 checkout 的稳定身份，形如 `wt_0123456789ab`：由
checkout 的规范化根路径派生，同一个 worktree 每次启动都相同，两个 worktree
一定不同。示例把身份放进数据目录和库文件名：

```yaml
env:
  DB_PATH: .berth/${worktree}/app.db
```

`api` 的 `prepare:` 在每次启动前执行 `migrate.py`，建表并写入一条种子记录；
迁移是幂等的，所以 `restart` 重跑也不会产生第二份数据。`api` 只读自己那份文件，
`/health` 返回实际数据库路径、迁移数和记录数。

`${worktree}` 是标识符，不是路径：把它嵌进相对路径（如上）或数据库名
（如 `app_${worktree}`）。它只在 `prepare`、`cmd` 和 `env` 里展开；其它
`${...}`（例如 `${HOME}`）仍按原样交给命令。它由 checkout 的路径派生，所以
移动 worktree 目录会得到新身份，旧数据不会被自动搬走。

## 从仓库运行

```sh
cd examples/worktree-data
oberth up --wait --json
oberth status --json
curl -s http://localhost:<status 中的实际端口>/health
```

`/health` 应返回类似：

```json
{"ok":true,"db":"/path/to/worktree/examples/worktree-data/.berth/wt_…/app.db",
 "migrations":1,"notes":1}
```

结束服务（不会删除数据）：

```sh
oberth down --force --json
```

## 看见两个 worktree 各自独立

在两个由你创建的 Git worktree 里分别进入本示例目录，各执行一次
`oberth up --wait --json` 和 `status --json`，然后分别读取 `api` 的 `/health`：

- 两个 `db` 路径不同，且都只包含一次迁移、一条记录；
- 只对其中一个执行 `down`，另一个的 `api` 仍在监听、`/health` 仍可访问；
- 对剩下那个执行 `restart --only api`，迁移不会重复，`migrations` 仍为 `1`。

`down` 只停进程，不删除任何一份数据库。要回收某个 worktree 的数据，由你在该
worktree 内删除 `.berth/`；示例不会自动删除用户数据。

`oberth` 本身不创建、修改或删除 Git worktree；这些 Git 写操作仍由你或现有
Git 工具负责。

## 常见问题

- `python3` 找不到：先安装 Python 3 并确认该终端能执行 `python3 --version`。
- 迁移失败：运行 `oberth logs api --once`，真实错误保留在那里，不把超时当作成功。
- `DB_PATH` 未设置：说明命令不是经 `oberth up` 启动的；直接运行脚本无法知道身份。
- 项目在 HOME 外：把示例放在你的 HOME 下，或明确审阅后使用 `--allow-outside-home`。

## 可重复验收

从仓库根目录，先构建，再运行：

```sh
mage buildEngine
python3 scripts/verify-example.py --binary "$PWD/bin/oberth"
```

脚本在两个一次性 Git worktree 中复制并运行本示例（以及 `hello-worktree`），
验证独立数据库、幂等迁移、单边 `down` 与自动端口，结束后回收自己创建的进程；
它不接触你日常的 daemon、账本或仓库写入。
