# macOS 首次使用 / Getting started

## 参考与取舍 / Reference boundaries

[Warp quickstart](https://docs.warp.dev/quickstart/) 提供渐进发现的交互参考。
当前已有分屏、命令面板、终端退出保护和显式恢复，不重复实现；首次使用指引独立跟踪 #47。
引导选择 [Apple TipKit](https://developer.apple.com/documentation/tipkit) 的原生提示，
而不是导入第三方 tour 引擎；macOS 14 与客户端最低版本一致。产品步骤使用 SwiftUI，
提示失败不能阻止用户打开帮助。不会启用 CloudKit 或遥测。

[Warp 许可证](https://github.com/warpdotdev/warp#license) 区分 MIT UI 框架与 AGPL 主体。
本项目只参考公开行为，不复制或翻译实现；继续使用固定版本的 MIT SwiftTerm。
TipKit 是系统框架，不把 Apple 框架代码作为 MIT 代码再分发。

## 操作顺序 / Walkthrough

1. **安装 / Install**：使用同一版本的 macOS 应用与 oberth 引擎。通过发布页或源码构建获取；
   安装和替换由人明确执行。不把阅读指引标为安装成功。外部 coding agent 按各自官方文档安装和登录。
2. **接入 worktree / Connect**：左栏 “+” 选择代码目录。已有清单直接读取；没有清单时审阅候选，
   明确确认才写入 oberth.yaml。模型只能起草，不能代替采纳；不会自动启动项目服务。
3. **核对事实 / Inspect**：服务页区分声明、运行和监听。Git 页只读；machine: 依赖不归本应用起停。
   失败与未知不是成功，停止请求不是退出证明。
4. **终端与 Agent / Work**：终端页按当前 worktree 新建 shell。Agent 消息框的 ⌘Enter 建立新会话，
   ⇧⌘Enter 转到当前原生会话；普通 Enter 换行。后续对话、登录与审批交给外部 CLI。
5. **恢复与隐私 / Resume**：恢复默认关闭，明确同意后只保存布局元数据。不保存消息、输出或 PID；
   恢复布局不会启动进程。Agent 续接须选择原始会话文件，不猜最近对话。

The guide is navigation and explanation, not an installer, environment validator, agent loop or cloud service.
Opening or reading it must not start a shell, launch a provider, write a manifest, change shell/provider
configuration, or claim that the machine is ready. Existing product actions keep their own explicit confirmation.

客户端操作与验证入口见 [client/macos/README.md](../client/macos/README.md)。
