# 让第一个服务跑起来

[English](quickstart.md) | 中文

## 开始前

先[安装 oberth](install.zh.md)，并准备好 Python 3。这个例子在 macOS/Linux 上只用 Python 标准库，不需要模型账号、容器或安装额外软件包。请在能查看本机监听端口的普通终端里操作。下面的命令会启动本地 HTTP 服务，请确认要运行后再执行；阅读文档本身不会执行它们。

## 建一个最小项目

新建一个空目录，把下面内容保存为 `oberth.yaml`。这是一份示例，不是对已有项目配置的修改建议。

```yaml
name: oberth-demo
services:
  - name: api
    cmd: python3 -u -m http.server ${port} --bind 127.0.0.1
    port: auto
```

`services` 列出这个目录要运行的服务。`cmd` 是启动命令；`port: auto` 为当前工作树预留一个端口，`${port}` 把端口号传给 Python。以后开第二份工作树时，不要把它换成各分支共用的固定端口。

## 启动、查看、停止

在这个目录中依次运行：

```sh
oberth up --wait
oberth status --json
oberth logs api --once
oberth down
```

只有满足配置的就绪条件，`up --wait` 才会成功返回。这份配置只要求 HTTP 进程开始监听，不代表业务功能已经健康。先从 `status --json` 读取实际端口，再在本机访问。`logs` 读取捕获的输出；`down` 停止配置里的服务，并交还自动预留的端口。

启动失败时，保留报错，按[常见问题](troubleshooting.zh.md)排查。不要猜 PID，也不要用会波及其他进程的批量终止命令。

## 换成自己的项目

可先用 `oberth init` 生成起始配置，再逐项检查 `oberth.yaml`：运行目录、命令、依赖、端口和健康检查必须符合项目实际。模型可以起草配置，但不能替你批准采用。字段说明见[配置参考](cli/declaration.zh.md#清单和状态的边界)，接入方法见[工具接入](integrations.zh.md)。

仓库里的 [Hello Worktree](../examples/hello-worktree/README.md)还演示了 API 健康检查和没有监听端口的后台任务。要同时运行两个分支，继续读[多工作树使用](worktrees.zh.md)。
