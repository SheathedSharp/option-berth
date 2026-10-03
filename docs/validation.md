# 验证范围

当前工作流均记录实际源码身份；检查结论只对应运行中的 commit，不沿用旧库或本机的历史结果。

| 工作流 | 平台与内容 |
|---|---|
| Engine verification | macOS / Ubuntu：原生构建、vet、全引擎单元、核心 race、真实服务场景、Mage 工作流回归、Python 辅助测试。 |
| Git read verification | macOS / Ubuntu：Git、worktree、归因与观察的重复测试和 race。 |
| Windows build verification | Windows x64：原生构建、vet、CLI/Jev 构建、version JSON/帮助页冒烟及事实/持久化测试；Ubuntu：Windows arm64 交叉构建。 |
| Client and brand verification | macOS：共享几何源与13个SVG一致性、8项生成器测试、原生客户端/ICNS构建及随包OFL检查。 |

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
