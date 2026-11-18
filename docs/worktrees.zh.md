# 同时使用多个工作树

[English](worktrees.md) | 中文

Git 工作树（worktree）是同一仓库的另一份独立检出目录。代码文件分开了，运行的服务也要能分清属于哪个目录。本页假设你已经审阅并提交了一份 `oberth.yaml`。

## 启动第二个分支

下面的例子从原项目目录创建一个新分支和工作树。请选未使用的分支名与目录名；这些 Git 命令会修改仓库。

```sh
git worktree add ../project-feature -b feature/demo
cd ../project-feature
oberth up --wait
oberth status --json
oberth down
cd -
git worktree remove ../project-feature
```

在哪份工作树里运行 `status`、`up`、`logs`、`down`，就操作哪份工作树。`port: auto` 让各工作树分别预留端口。固定端口仍可能冲突：oberth 不会偷偷换端口，也不会停止占用端口的无关进程。

## 隔离到什么程度

这里的隔离指运行归属和启停目标，不是操作系统沙箱。两份工作树仍共享你的账号、文件系统、Docker，以及服务命令能够访问的资源。应用数据目录和数据库名也要自行区分；`${worktree}` 是由检出路径确定的稳定标识，不是分支名。

`machine:` 依赖只用于观察。`workspace:` 地址可以读取明确关联的另一份工作树已发布的地址，但不会把对方服务加入当前项目的启停范围。写法见[接入示例](integrations.zh.md)。

## 先停服务，再删目录

优先在目录仍存在时执行 `down`，再用 Git 移除工作树。如果目录已经删了，用 `oberth worktrees --gone --json` 查保留的身份记录，选准身份后按[命令参考](cli/operations.zh.md)使用 `down --identity`。这里的操作都不会删除应用数据。

多个工作树重名时，不要只凭组名猜目标：选择有歧义会报错，不等于允许停止所有同名项。编程助手应使用[配套 skill](https://github.com/SheathedSharp/option-berth-skill)，并明确指定工作树范围。
