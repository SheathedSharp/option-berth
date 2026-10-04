import AppKit
import SwiftUI
import TipKit

/// TipKit owns tip eligibility and dismissal. This is not a second tour engine,
/// an installer, or a record of the machine's readiness. No CloudKit is configured.
@MainActor
enum OnboardingTips {
    private static var attempted = false
    private(set) static var enabled = false

    @discardableResult
    static func configure() -> Bool {
        guard !attempted else { return enabled }
        attempted = true
        do {
            let environment = ProcessInfo.processInfo.environment
            let base = environment["BERTH_HOME"].flatMap { $0.isEmpty ? nil : $0 }
                ?? FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".option-berth").path
            guard base.hasPrefix("/"), !base.utf8.contains(0) else { return false }
            let directory = URL(fileURLWithPath: base).appendingPathComponent("client-tips", isDirectory: true)
            try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true,
                                                    attributes: [.posixPermissions: 0o700])
            let attributes = try FileManager.default.attributesOfItem(atPath: directory.path)
            guard attributes[.type] as? FileAttributeType == .typeDirectory,
                  (attributes[.posixPermissions] as? NSNumber)?.intValue == 0o700 else { return false }
            try Tips.configure([.datastoreLocation(.url(directory)), .displayFrequency(.immediate)])
            enabled = true
        } catch {
            // Help remains available without exposing private filesystem errors.
            enabled = false
        }
        return enabled
    }
}

struct FirstWorktreeTip: Tip {
    var title: Text { Text("从一份 worktree 开始 / Start here") }
    var message: Text? { Text("先了解清单、服务和原生 Agent 的边界，再接入项目。 / Learn the workflow before connecting a project.") }
    var image: Image? { Image(systemName: "sparkle.magnifyingglass") }
    var actions: [Tips.Action] { [Tips.Action(id: "guide", title: "打开使用指引 / Getting started")] }
}

enum GettingStartedStep: Int, CaseIterable, Identifiable {
    case setup, worktree, facts, agent, recovery
    var id: Int { rawValue }
    var title: String {
        switch self {
        case .setup: return "准备本地工具 / Prepare your tools"
        case .worktree: return "接入一份代码 / Connect a worktree"
        case .facts: return "先看事实，再起停 / Inspect before acting"
        case .agent: return "Terminal 与 Agent 各司其职"
        case .recovery: return "恢复布局，不自动复活进程"
        }
    }
    var symbol: String {
        switch self {
        case .setup: return "shippingbox"
        case .worktree: return "folder.badge.plus"
        case .facts: return "server.rack"
        case .agent: return "terminal"
        case .recovery: return "clock.arrow.circlepath"
        }
    }
    var chinese: String {
        switch self {
        case .setup: return "使用同一版本的 macOS 应用与 oberth 引擎。更新入口只查看发布信息，不会自动安装。\n\n需要 Agent 时，再按所选工具的官方文档安装和登录 OpenCode、Codex、Claude Code、DeepSeek Harness 或 Pi；普通终端不要求 Agent 账号。"
        case .worktree: return "左栏“+”选择代码目录。已有 oberth.yaml 时读取它；没有清单时先审阅候选，再明确确认写入。\n\nAgent 只能起草清单，不能替你采纳。接入项目不等于启动服务；两个 worktree 的会话与运行事实保持分开。"
        case .facts: return "⌘1 查看清单、运行记录与实际监听，⌘2 查看只读 Git。服务的失败、未知和观察错误都不是成功。\n\n只起停项目明确声明的服务，machine: 依赖只读。停止请求不是退出证明；检查实际退出和日志后再继续。"
        case .agent: return "⌘3 打开终端页，点击新建才启动 shell。切换 worktree 不会迁移已有会话。\n\n在 Agent 消息框中，⌘Enter 用初始消息创建新会话，⇧⌘Enter 进入当前原生会话，Enter 换行。后续输入、登录与审批由外部 Agent 处理，不绕过沙箱或审批。"
        case .recovery: return "恢复默认关闭。明确允许后，只保存布局和 provider 等元数据，不保存消息、终端输出或 PID；恢复布局不会启动 shell 或 Agent。\n\nAgent 续接需选择原始会话文件，不猜最近对话。命令块默认关闭且仅在内存保存。退出应用前先结束自有活动会话并确认退出。"
        }
    }
    var english: String {
        switch self {
        case .setup: return "Use a matching app and engine release. Install and authenticate external agents yourself, only when needed. Reading this guide does not validate your installation."
        case .worktree: return "Choose a code directory with +. Review and explicitly accept a new manifest. Connecting a worktree never starts its services."
        case .facts: return "Cmd+1: services. Cmd+2: read-only Git. Unknown is not success; a stop request is not an exit. Machine dependencies remain read-only."
        case .agent: return "Cmd+3: console. In the composer, Cmd+Return starts a new agent session; Shift+Cmd+Return hands off to the native session. Continue there, with the provider's own approvals."
        case .recovery: return "Recovery is opt-in metadata only, never automatic process resurrection. Resume an agent using an explicitly selected original file. No cloud sync or extra conversation store."
        }
    }
}

struct GettingStartedPage: View {
    let step: GettingStartedStep
    var body: some View {
        VStack(alignment: .leading, spacing: 18) {
            Image(systemName: step.symbol).font(.system(size: 26)).foregroundStyle(Ink.accent)
            Text(step.title).font(Face.sans(19, .semibold)).fixedSize(horizontal: false, vertical: true)
            Text(step.chinese).font(Face.sans(12)).fixedSize(horizontal: false, vertical: true)
            Text(step.english).font(Face.sans(11)).foregroundStyle(Ink.inkMuted)
                .fixedSize(horizontal: false, vertical: true)
        }.frame(maxWidth: .infinity, alignment: .leading)
    }
}

/// A replayable help document. Navigation does not execute product actions.
/// Only the explicit Connect button requests the existing directory picker.
struct GettingStartedGuide: View {
    var frozen = false
    let onClose: () -> Void
    let onConnect: () -> Void
    @State private var step: GettingStartedStep

    init(initialStep: GettingStartedStep = .setup, frozen: Bool = false,
         onClose: @escaping () -> Void, onConnect: @escaping () -> Void) {
        _step = State(initialValue: initialStep)
        self.frozen = frozen; self.onClose = onClose; self.onConnect = onConnect
    }
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                Label("使用指引 / Getting started", systemImage: "map").font(Face.sans(13, .semibold))
                Spacer()
                Text("\(step.rawValue + 1) / \(GettingStartedStep.allCases.count)").font(Face.mono(11))
                    .accessibilityIdentifier("guide.progress")
            }
            Hairline()
            if frozen { GettingStartedPage(step: step).frame(maxHeight: .infinity, alignment: .top) }
            else { ScrollView { GettingStartedPage(step: step).padding(.trailing, 6) } }
            Text("阅读完成 ≠ 环境验证通过 / Reading is not a readiness check")
                .font(Face.sans(10)).foregroundStyle(Ink.inkMuted)
            Hairline()
            HStack {
                Button("稍后 / Later", action: onClose).keyboardShortcut(.cancelAction)
                    .accessibilityIdentifier("guide.close")
                if step == .worktree {
                    Button("接入项目… / Connect…", action: onConnect).accessibilityIdentifier("guide.connect")
                }
                Spacer()
                Button("上一步 / Back") { step = GettingStartedStep(rawValue: step.rawValue - 1) ?? .setup }
                    .disabled(step == .setup).accessibilityIdentifier("guide.back")
                Button(step == .recovery ? "读完 / Done" : "下一步 / Next") {
                    if step == .recovery {
                        if OnboardingTips.enabled { FirstWorktreeTip().invalidate(reason: .actionPerformed) }
                        onClose()
                    } else { step = GettingStartedStep(rawValue: step.rawValue + 1) ?? .recovery }
                }.keyboardShortcut(.defaultAction).accessibilityIdentifier("guide.next")
            }.controlSize(.small)
        }.padding(20).frame(width: 600, height: 470)
            .background(Ink.canvas).foregroundStyle(Ink.ink)
    }
}
