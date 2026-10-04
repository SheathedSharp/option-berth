# 验证范围

当前工作流均记录实际源码身份；检查结论只对应运行中的 commit，不沿用旧库或本机的历史结果。

| 工作流 | 平台与内容 |
|---|---|
| Engine verification | macOS / Ubuntu：原生构建、vet、全引擎单元、核心 race、真实服务场景、Mage 工作流回归、Python 辅助测试。 |
| Git read verification | macOS / Ubuntu：Git、worktree、归因与观察的重复测试和 race。 |
| Windows build verification | Windows x64：原生构建、vet、CLI/Jev 构建、version JSON/帮助页冒烟及事实/持久化测试；Ubuntu：Windows arm64 交叉构建。 |
| Client and brand verification | macOS：共享几何源与13个SVG一致性、8项生成器测试、原生客户端/ICNS构建、PTY/输入框/会话导航回归及随包OFL检查。 |

CLI、Mage 与品牌迁移分别审查、验证、合并，没有引入旧私人 Git 祖先。
Windows 的编译/冒烟与所选原生测试，不等于全部服务起停、Windows GUI 或 arm64 原生执行验收。

## 本机复现

Go 版本按 `engine/go.mod`。服务测试使用独立 HOME/BERTH_HOME、socket 和自建服务。
macOS Unix socket 路径较短，测试不能把长测试名和多层临时目录全部拼到 socket 路径。

```sh
cd engine
go build ./...
go vet ./...
go test -count=1 -timeout=8m ./...
go test -race -count=1 -timeout=10m ./internal/claims ./internal/runs ./internal/spawn ./internal/daemon/... ./internal/git ./internal/groups ./internal/ports ./internal/scanner ./internal/servicefacts ./internal/state ./internal/store ./internal/killer
go test -tags integration -count=1 -timeout=8m ./internal/scenario/...
```

Mage 与品牌检查分别位于各自PR及CI。品牌迁移已生成10个冻结界面与7档图标，并查看代表性
空态、服务态、字号页和图标；没有把离屏渲染称为真实Dock、菜单或全部交互验收。
构建、截图、安装和服务起停是不同副作用，普通检查不操作日常用户服务。

## 不作出的保证

GUI全部交互、签名安装包和长期负载仍需独立验收；没有可靠对照不宣传整体加速或长期无泄漏。
自动密钥扫描、许可证输入保留和CI成功各自有边界，不能替代安全审查或完整分发审核。

## 终端工作区的分层验证

`TerminalChecks` 使用独立目录和自有子进程检查 cwd/PWD、退出回执、1 MiB 输出、24 次会话
回收、工作区草稿/模式/选中项保留、重命名约束、项目移除后会话可达性，以及混合停止状态下
只升级已有 stopping 会话。原生 NSWindow/NSSplitView 分别验证分隔尺寸、每栏焦点与输入、
终端历史查找和字号变化，不读取或修改用户剪贴板。

`AgentChecks` 默认使用合成 planner；设置 `BERTH_AGENT_TEST_BINARY` 后接入实际 oberth，
对照五个 provider 的 native/task 参数计划，再用临时 provider 替身验证原生 PTY 交接。
这不是登录五个真实模型账号，也不等于验收所有 provider 版本。

`11-console-split` 冻结图只表达两栏布局；原生分隔控件和终端内容必须由独立原生检查验证。
完整窗口的鼠标菜单、系统输入法、辅助功能及长时间负载仍需继续真实使用验收。
不要将“页面切换保留内存状态”写成“应用重启恢复进程与会话”。

## 有限 CLI 回收回归

`CLIIOTests.swift` 由 Client workflow 和发版检查直接编译运行，使用自身可执行文件作为
合成 CLI，不启动用户服务或真实 provider。覆盖双流压力、输出/进度帧上限、UTF-8、隐私
诊断、信号退出、拒绝 TERM、关闭管道后挂起、直接子进程改变进程组、后代持有/关闭管道、
主动脱组的有界夹具、预取消、非法输入与时限、工作目录，以及 12 次跨线程取消后的直接
子进程 reap 和文件描述符稳定性。后代退出用进程状态验证，不把 kill 返回值当作退出证明。

```sh
swiftc client/macos/Sources/CLI.swift client/macos/Sources/DaemonLaunch.swift \
  client/macos/Tests/CLIIOTests.swift -o /tmp/oberth-cli-io-tests
/tmp/oberth-cli-io-tests
```

这些是有限合成回归，不是所有 provider、内核不可杀状态或长期资源趋势的验收。
