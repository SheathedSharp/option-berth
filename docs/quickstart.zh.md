# 让第一个服务跑起来

[English](quickstart.md) | 中文

一个文件，一个 HTTP 服务：把它启动、读出 oberth 观察到的结果，再把它停掉。

## 开始前

先[安装 oberth](install.zh.md)，并准备好 Python 3。这个例子在 macOS/Linux 上只用 Python 标准库：不需要模型账号、容器或额外软件包。请在能查看本机监听端口的普通终端里操作。

## 建一个最小项目

新建一个空目录，把下面内容保存为 `oberth.yaml`：

```yaml
name: oberth-demo
services:
  - name: api
    cmd: python3 -u -m http.server ${port} --bind 127.0.0.1
    port: auto
```

`services` 列出这个目录要运行的服务。`cmd` 是启动命令；`port: auto` 为当前工作树预留一个端口，`${port}` 把分配到的端口号传给 Python。以后开第二份工作树时继续用这种预留，不要换成各分支共用的固定端口。

## 启动，然后读 oberth 看到了什么

在这个目录中依次运行：

```sh
oberth up --wait
oberth runtime inspect --json
oberth status --json
oberth logs api --once
oberth down
```

只有配置的就绪条件满足，`up --wait` 才会成功返回。这里的就绪只表示 HTTP 进程开始监听，不代表业务功能已经健康。`runtime inspect` 把整个工作树打印成一份文档：`coverage` 说明这一轮能观察到多少，`health` 给出整棵工作树的结论，`diagnosis.next_actions` 列出下一步能做什么，以及各自的 `safety` 等级。`read_only` 的动作可以直接执行，`local_restart` 会重启本地服务，`human_decision` 要由你决定。`status` 按服务给出同一批事实，`diagnosis` 字段也是同一个答案。

在本机访问之前，先从 `status --json` 读出实际分配的端口。`logs api --once` 打印一次捕获到的输出。`down` 停掉配置里的服务，并交还自动预留的端口。

## 出问题的时候

保留报错原文，按[常见问题](troubleshooting.zh.md)逐项排查。不要猜 PID，也不要用会波及别的进程的批量终止命令；端口属于别人是一个需要读出来的事实，不是要清掉的进程。

## 换成自己的项目

`oberth init` 会写出一份起始配置。然后对着项目逐项检查 `oberth.yaml`：运行目录、命令、依赖、端口和健康检查。模型可以起草清单，采用它的只能是人。字段说明见[配置参考](cli/declaration.zh.md#清单和状态的边界)，编排工具的接入方法见[工具接入](integrations.zh.md)。

仓库里的 [Hello Worktree](../examples/hello-worktree/README.md)还演示了 API 健康检查和没有监听端口的后台任务。要同时运行两个分支，继续读[多工作树使用](worktrees.zh.md)。
