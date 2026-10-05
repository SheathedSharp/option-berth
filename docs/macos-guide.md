# macOS 首次使用 / Getting started

## 参考与取舍

参考 [Driver.js 的配置与交互](https://driverjs.com/docs/configuration) 的控件聚光、遮罩、
定位说明、进度和跳过；参考 [Instructions](https://github.com/ephread/Instructions) 的真实
视图锚定与布局变化处理。后者已声明 deprecated，且属于 UIKit/iOS，不作为本项目依赖。
不引入 WebView、JavaScript tour 引擎、CloudKit 或遥测。

本客户端直接用 SwiftUI 收集真实控件坐标、绘制偶奇挖空遮罩，使用 AppKit 原生按钮管理
键盘与焦点。通过 [transformAnchorPreference](https://developer.apple.com/documentation/swiftui/view/transformanchorpreference(key:value:transform:))
保留父子控件的锚点，不让项目列表容器覆盖内部接入按钮的几何信息。
动画仅作用于位置和挖空，不混合两步文字；系统 Reduce Motion 关闭定位动画。
没有复用第三方实现代码或增加依赖，沿用项目及现有依赖的许可边界。

## 首次与重看

第一次打开工作区时自动出现一次。本地 `workspaceTourPresented` 在**展示时**立即持久化，
不是等完成才记忆；跳过、窗口关闭、重启或升级不再自动弹出。不按发布版本重置资格。
已有旧 `$BERTH_HOME/client-tips` 元数据时，保守认定为已有用户，不打扰；不删除该目录。
从空态、“更多”或 Help 菜单可以显式重看。

这是覆盖在实际页面上的导览，不是独立文字说明页。每一步只有对应真实控件存在且可见时
才出现；空工作区不伪造项目或服务页签。接入项目后，手动重看可覆盖新增的真实页签。
遮罩包含挖空区的点击拦截，底层工作区不可误操作。Return 继续，Escape 跳过，左右键
切换步骤，Tab/Shift-Tab 在导览按钮间循环。结束后恢复同一窗口仍存活的原焦点。

## 页面上会介绍什么

接入入口用于选择项目并审查清单；项目列表用于切换 Worktree 上下文。已有项目的服务、Git
和终端页签共享同一项目目录，Git 只读，机器级依赖不会被当作项目服务起停。
命令面板帮助查找动作，会话入口管理 Shell 与外部 Agent，恢复入口先展示建议再由人确认。
更多菜单保留设置、检查更新与重看入口。

安装和替换见 [macOS 安装说明](macos-install.md)。导览不会安装或登录外部工具，不创建假项目，
不写清单、不启动项目服务、Shell 或 Agent，也不把读完标成环境验证通过。
后台启动仍由现有工作区生命周期负责；导览不另建启动器或业务状态。

## Verification boundaries

Native checks use isolated preferences, HOME and BERTH_HOME, including separate child processes to
verify durable first-use eligibility. Mouse/keyboard tests exercise the actual workspace overlay and
capture synthetic paper/midnight windows. Frozen renders supplement layout review only.
The reduced-motion policy is tested without changing the user's system setting. Complete VoiceOver,
all input methods and all customized font configurations are not claimed as covered.

客户端操作与验证入口见 [client/macos/README.md](../client/macos/README.md)。
