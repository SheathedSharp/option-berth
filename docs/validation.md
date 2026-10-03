# 验证范围

日期：2026-10-04。开发预览版，不提供稳定性或长期性能保证。
本轮代码身份为 `engine/internal` 内容树 `8167f3343d6c39787e98e07062e1d7843a750893`。

## 本轮结果

| 检查 | macOS arm64 / Go 1.25.0 |
|---|---|
| `go build ./...` | 通过 |
| `go vet ./...` | 通过 |
| `go test -count=1 -timeout=5m ./...` | 通过 |
| 关键包 `go test -race -count=1 -timeout=5m` | 通过 |
| `go test -tags integration -count=1 -timeout=5m ./internal/scenario/...` | 完整场景通过 |
| worker 原失败场景连续三次执行 | 通过 |

race 包包括 claims、runs、spawn、daemon/...、git、groups、ports、scanner、servicefacts、
state、store、killer。测试使用隔离 HOME、账本、socket 和自建服务，不操作日常服务。
首次运行因额外嵌套的临时目录使一个 Unix socket 路径过长；缩短测试根后完整复测通过。

Linux 前一轮代码曾通过全引擎单元与关键包 race。本轮新代码的本机 Linux 复测在容器
编译器下载阶段超时，尚未执行，不能沿用前轮结果称其通过；专用容器已回收。
公开 CI 配置继续覆盖 Ubuntu 与 macOS，以具体运行结果为准。Windows 未做原生验证
或交叉构建。客户端 GUI、安装包和长期负载不在本轮验收范围内。

## worker 修复

daemon 重启后导入的运行没有当前 daemon 的 child reaper。停止后先按真实存活证据
清理登记，再执行既有强制扫描并发布，避免启动查重继续消费旧的 Running 快照。
新增回归确认清理先于发布，dry-run 不清理、不扫描；实际 scoped restart 场景通过。
没有新增后台任务、第二份运行事实或协议字段，也不把信号回执当作退出证明。

## 源码公开检查

当前源码导出不包含旧 Git 历史。Gitleaks 8.30.1 的目录扫描未检出密钥；另检查个人路径、
邮箱、URL 凭证、符号链接与图片文本/EXIF 元数据。扫描无命中不构成不存在任何漏洞或
隐私问题的绝对保证。原始报告和日志不入仓库。

许可证及分发范围见 [THIRD_PARTY_NOTICES.md](../THIRD_PARTY_NOTICES.md)，
系统权限、IO 和进程身份的已知边界见 [architecture.md](architecture.md)。
