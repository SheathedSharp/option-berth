import Foundation
import CoreGraphics


/// Presentation eligibility is deliberately not keyed to an app version.
@MainActor
enum TourFirstUse {
    static let key = "workspaceTourPresented"
    static func claim(defaults: UserDefaults = .standard, legacyUser: Bool? = nil) -> Bool {
        guard !defaults.bool(forKey: key) else { return false }
        // Claim at presentation, not completion: closing a window or skipping
        // must not replay the tour after restart or in another window.
        defaults.set(true, forKey: key)
        return !(legacyUser ?? legacyStoreExists())
    }
    static func legacyStoreExists() -> Bool {
        let home = ProcessInfo.processInfo.environment["BERTH_HOME"].flatMap { $0.isEmpty ? nil : $0 }
            ?? FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".option-berth").path
        // Old clients configured TipKit on opening the workspace. Conservatively
        // migrate existing users without a surprise automatic tour on upgrade.
        return FileManager.default.fileExists(atPath: home + "/client-tips")
    }
}

enum TourTarget: Int, CaseIterable, Identifiable {
    case connect, worktrees, facts, commands, sessions, recovery, more
    var id: Int { rawValue }
    var title: String {
        switch self {
        case .connect: return "接入你的第一个 Worktree"
        case .worktrees: return "围绕 Worktree 切换上下文"
        case .facts: return "服务、Git 与终端各司其职"
        case .commands: return "从这里快速找到操作"
        case .sessions: return "会话由你开启，也由你结束"
        case .recovery: return "恢复前，先确认"
        case .more: return "随时回来，不再打扰"
        }
    }
    var chinese: String {
        switch self {
        case .connect: return "用这个入口选择项目。先审查清单，再确认接入；导览不会替你创建项目或启动服务。"
        case .worktrees: return "已接入的项目会列在这里。选择一个项目后，服务、Git 和终端共享该 Worktree 上下文。"
        case .facts: return "服务页展示运行事实，Git 页只读浏览。终端与外部 Agent 使用同一项目目录，不另建 Agent 系统。"
        case .commands: return "搜索工作区动作，也可使用 ⇧⌘P。结束导览后再试；现在不会执行任何动作。"
        case .sessions: return "查看已有 Shell 与 Agent 会话。Agent 需自行安装登录，审批和后续对话留在原生界面。"
        case .recovery: return "重开应用不会自动重启旧任务。这里先列出恢复建议，由你确认后再继续。"
        case .more: return "设置、更新检查和使用指引都在这里。导览仅首次自动出现，之后可从这里或帮助菜单重看。"
        }
    }
    var english: String {
        switch self {
        case .connect: return "Choose a project here, review its manifest, then confirm. This tour creates no projects or services."
        case .worktrees: return "Registered projects appear here. Services, Git and terminal share the selected worktree."
        case .facts: return "Services show runtime facts; Git is read-only. Terminal and external agents use the same directory."
        case .commands: return "Find workspace actions here or with Shift–Command–P. Try them after closing the tour."
        case .sessions: return "Manage existing shell and agent sessions. Install agents yourself; approvals stay in their native UI."
        case .recovery: return "Reopening the app does not restart old tasks. Review recovery suggestions before continuing."
        case .more: return "Settings, updates and this guide live here. Automatic guidance appears once; Help can replay it."
        }
    }
}

/// Geometry is shared by the native overlay and deterministic resize checks.
enum TourLayout {
    static let cardSize = CGSize(width: 340, height: 282)
    static func visible(_ frame: CGRect, in size: CGSize) -> CGRect? {
        guard [frame.minX, frame.minY, frame.width, frame.height, size.width, size.height].allSatisfy(\.isFinite),
              frame.width > 0, frame.height > 0 else { return nil }
        let clipped = frame.intersection(CGRect(origin: .zero, size: size).insetBy(dx: 6, dy: 6))
        return clipped.isNull || clipped.width < 2 || clipped.height < 2 ? nil : clipped
    }
    static func card(near target: CGRect, in size: CGSize) -> CGRect {
        let safe = CGRect(origin: .zero, size: size).insetBy(dx: 16, dy: 16)
        let width = min(cardSize.width, max(1, safe.width))
        let height = min(cardSize.height, max(1, safe.height))
        func clamped(_ x: CGFloat, _ y: CGFloat) -> CGRect {
            CGRect(x: min(max(x, safe.minX), safe.maxX - width),
                   y: min(max(y, safe.minY), safe.maxY - height), width: width, height: height)
        }
        let candidates = [clamped(target.maxX + 18, target.midY - height / 2),
                          clamped(target.minX - width - 18, target.midY - height / 2),
                          clamped(target.midX - width / 2, target.maxY + 18),
                          clamped(target.midX - width / 2, target.minY - height - 18)]
        return candidates.first { !$0.intersects(target.insetBy(dx: -8, dy: -8)) } ?? candidates[2]
    }
}
