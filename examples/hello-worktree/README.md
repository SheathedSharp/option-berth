# 第一次跑通 / Hello Worktree

这是一份人可以直接审阅的示例清单，不是对你的其他项目进行猜测。
只需已安装的 `oberth`、Python 3；没有 npm/pip 依赖、容器、模型账号或 API key。
HTTP 服务只监听 `127.0.0.1`，不提供文件浏览，也不向外部网络发送请求。

## 从仓库运行

```sh
cd examples/hello-worktree
oberth up --wait --json
oberth status --json
oberth logs worker --once
```

`api` 使用自动端口并通过 `/health` 检查；`worker` 没有端口，等待 API 健康后启动，
通过 `${api.url}` 获得 API 地址。打开 `status` 中当前服务的 URL，响应包含实际端口。
日志应出现 `worker: API health=200`。结束时在同一目录运行：

```sh
oberth down --force --json
```

不要使用全机 `kill` 或停止普通用户 daemon 来清理这个示例。
`down` 只处理此清单所属 worktree 的项目服务。

## 看见 worktree 隔离

先在两个由你创建的 Git worktree 内分别进入 `examples/hello-worktree`，再分别执行上述
`up` 和 `status`。两个 checkout 的 API 应获得不同端口。只在其中一个执行 `down`，
另一个的 API 与 worker 应继续运行。这不是容器或操作系统安全沙箱；它验证的是运行归属和生命周期隔离。

`oberth` 本身不创建、修改或删除 Git worktree；这些 Git 写操作仍由你或现有 Git 工具负责。

## 常见问题

- `python3` 找不到：先安装 Python 3 并确认该终端能执行 `python3 --version`；本示例不会自动安装工具链。
- 健康等待失败：运行 `oberth logs api --once`，保留真实错误，不把超时当作成功。
- 项目在 HOME 外：把示例放在你的 HOME 下，或明确审阅后选择已有的 `--allow-outside-home`；不默认绕过限制。
- 独立演示/CI：使用下面的验收脚本。它创建临时 HOME、状态、socket 和两个 Git worktree，完成后清理自己创建的进程。

## 可重复验收

从仓库根目录，先构建，再运行：

```sh
mage buildEngine
python3 scripts/verify-example.py --binary "$PWD/bin/oberth"
```

脚本复制的就是本目录文件，不再维护另一份类似示例。它验证双 worktree、健康、自动端口、
无端口 worker 和单边停止保护。macOS/Linux 有原生 CI；没有据此声称 Windows 生命周期已通过。
