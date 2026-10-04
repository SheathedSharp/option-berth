import AppKit
import Combine
import BerthTerminal
import SwiftUI

@MainActor
final class WorkspaceRecovery: ObservableObject {
    static let shared = WorkspaceRecovery()
    @Published private(set) var enabled: Bool
    @Published private(set) var archive: WorkspaceArchive?
    @Published private(set) var remembered: [SavedSession] = []
    @Published private(set) var reviewPending = false
    @Published private(set) var problem: String?
    private let defaults: UserDefaults
    private let store: WorkspaceArchiveStore
    private let sessions: TerminalSessions
    private var timer: Timer?
    private var subscriptions: [ObjectIdentifier: AnyCancellable] = [:]
    private var sessionSubscription: AnyCancellable?
    private var loaded = false
    private let key = "workspace.restore-layout-consent.v1"

    init(defaults: UserDefaults = .standard, directory: URL? = nil, sessions: TerminalSessions? = nil) {
        self.defaults = defaults; self.sessions = sessions ?? .shared
        let env = ProcessInfo.processInfo.environment
        let base = env["BERTH_HOME"].flatMap { $0.hasPrefix("/") ? URL(fileURLWithPath: $0) : nil }
            ?? FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".option-berth")
        store = WorkspaceArchiveStore(directory: directory ?? base.appendingPathComponent("client-recovery"))
        enabled = defaults.bool(forKey: key)
        sessionSubscription = self.sessions.objectWillChange.sink { [weak self] in
            Task { @MainActor [weak self] in self?.scheduleSave() }
        }
    }
    func loadOnce() {
        guard !loaded else { return }; loaded = true
        guard enabled else { return }
        do { archive = try store.read(); reviewPending = archive != nil; problem = nil }
        catch { problem = error.localizedDescription; reviewPending = true }
    }
    func enable() {
        do {
            let snapshot = try sessions.snapshot(remembered: remembered)
            try store.write(snapshot)
            defaults.set(true, forKey: key); enabled = true; loaded = true
            archive = snapshot; reviewPending = false; problem = nil
        } catch { problem = error.localizedDescription }
    }
    func disableAndDelete() {
        timer?.invalidate(); timer = nil
        do {
            try store.remove()
            defaults.removeObject(forKey: key); enabled = false
            discardRememberedReferences()
            archive = nil; reviewPending = false; problem = nil
        } catch { problem = error.localizedDescription }
    }
    // Discarding historical entries must also prune their layout references.
    // Keep current registry-owned sessions and never signal or stop a process.
    private func discardRememberedReferences() {
        let current = Set(sessions.sessions.map(\.id))
        for record in remembered where !current.contains(record.id) {
            sessions.workspace(record.worktree).forget(record.id)
        }
        remembered = []
    }
    func watch(_ workspace: ConsoleWorkspace) {
        let id = ObjectIdentifier(workspace)
        guard subscriptions[id] == nil, subscriptions.count < 128 else { return }
        subscriptions[id] = workspace.objectWillChange.sink { [weak self] in
            Task { @MainActor [weak self] in self?.scheduleSave() }
        }
    }
    func scheduleSave() {
        guard enabled, loaded, !reviewPending else { return }
        timer?.invalidate()
        timer = Timer.scheduledTimer(withTimeInterval: 0.4, repeats: false) { [weak self] _ in
            Task { @MainActor [weak self] in self?.saveNow() }
        }
    }
    func saveNow() {
        timer?.invalidate(); timer = nil
        guard enabled, !reviewPending else { return }
        do { let snapshot = try sessions.snapshot(remembered: remembered); try store.write(snapshot); archive = snapshot; problem = nil }
        catch { problem = error.localizedDescription }
    }
    func restoreLayout() {
        guard let archive, reviewPending else { return }
        do {
            try sessions.restoreMetadata(archive)
            subscriptions.removeAll()
            remembered = archive.sessions; reviewPending = false; problem = nil
            // Restoration replaces ConsoleWorkspace instances. Observe those
            // instances now, not only after a later page's onAppear callback.
            for workspace in archive.workspaces { watch(sessions.workspace(workspace.root)) }
            // Never launch a shell/agent or mark an old process as running.
        } catch { problem = error.localizedDescription }
    }
    func useCurrentLayout() {
        discardRememberedReferences()
        reviewPending = false; problem = nil; saveNow()
    }
    func newShell(for record: SavedSession, executable: String? = nil, environment: [String: String]? = nil) {
        guard !reviewPending, record.kind == "terminal", remembered.contains(record) else { return }
        do {
            let state = sessions.workspace(record.worktree)
            // Registry.add selects the new session. Keep the saved layout until
            // its old reference has been replaced so other placeholders survive.
            let terminal = state.terminalLayout, agent = state.agentLayout
            let session = try sessions.add(worktree: record.worktree, title: record.title,
                                           executable: executable ?? TerminalSession.shell, arguments: ["-i"], environment: environment)
            state.terminalLayout = terminal; state.agentLayout = agent
            try sessions.replaceRestoredReference(record.id, with: session)
            remembered.removeAll { $0.id == record.id }
            problem = nil; saveNow()
        } catch { problem = error.localizedDescription }
    }
    func forget(_ record: SavedSession) {
        guard remembered.contains(record) else { return }
        sessions.workspace(record.worktree).forget(record.id)
        remembered.removeAll { $0.id == record.id }; saveNow()
    }
}

struct WorkspaceRecoverySheet: View {
    @ObservedObject var recovery: WorkspaceRecovery
    let openWorktree: (String) -> Void
    @Environment(\.dismiss) private var dismiss
    @State private var confirmingEnable = false
    @State private var reopening: SavedSession?
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Label("工作区恢复 / Workspace recovery", systemImage: "clock.arrow.circlepath").font(Face.sans(14, .semibold))
                Spacer(); Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction)
            }
            Text("只保存 worktree 路径、会话名称、模式与布局。不保存消息、命令、终端输出、环境变量或凭证。恢复入口不会自动启动进程。")
                .font(Face.sans(11)).foregroundStyle(Ink.inkMuted).fixedSize(horizontal: false, vertical: true)
            if !recovery.enabled {
                Button("允许保存布局元数据…") { confirmingEnable = true }
            } else {
                HStack {
                    if recovery.reviewPending {
                        Button("恢复布局，不启动进程") { recovery.restoreLayout() }.disabled(recovery.archive == nil)
                        Button("放弃旧布局，保留当前") { recovery.useCurrentLayout() }
                    } else { Button("立即保存布局") { recovery.saveNow() } }
                    Spacer()
                    Button("关闭恢复并删除保存数据", role: .destructive) { recovery.disableAndDelete() }
                }
            }
            if recovery.reviewPending {
                Text("上次布局等待确认。确认前不会被当前空工作区自动覆盖。")
                    .font(Face.sans(11)).foregroundStyle(Ink.ink)
            }
            ScrollView {
                VStack(alignment: .leading, spacing: 10) {
                    ForEach(recovery.remembered) { record in
                        VStack(alignment: .leading, spacing: 5) {
                            HStack {
                                Label(record.title, systemImage: record.kind == "terminal" ? "terminal" : "bubble.left.and.bubble.right")
                                Text("历史入口 · 非运行进程").font(Face.sans(10)).foregroundStyle(Ink.inkFaint)
                                Spacer()
                            }
                            Text(record.worktree).font(Face.mono(10)).lineLimit(1).truncationMode(.middle)
                            HStack {
                                Button("打开工作区") { openWorktree(record.worktree); dismiss() }
                                if record.kind == "terminal" { Button("新建替代 Shell…") { reopening = record } }
                                else { Text("对话续接需选择 provider 原始会话文件").font(Face.sans(10)).foregroundStyle(Ink.inkMuted) }
                                Spacer(); Button("移除入口") { recovery.forget(record) }
                            }
                        }.padding(10).background(Ink.surface).clipShape(RoundedRectangle(cornerRadius: 6))
                    }
                    if recovery.remembered.isEmpty {
                        Text(recovery.reviewPending ? "尚未应用上次布局。" : "没有待打开的历史会话入口。")
                            .foregroundStyle(Ink.inkMuted).padding(.vertical, 14)
                    }
                }
            }.frame(minHeight: 160, maxHeight: 320)
            Text("旧 PTY 不会按保存的 PID 重新附着；旧进程的存活状态未知。命令/对话历史仍由 shell 或外部 agent 管理。")
                .font(Face.sans(10)).foregroundStyle(Ink.inkFaint).fixedSize(horizontal: false, vertical: true)
            if let problem = recovery.problem { Text(problem).font(Face.sans(11)).foregroundStyle(Ink.ink) }
        }.padding(18).frame(width: 680).background(Ink.canvas).foregroundStyle(Ink.ink)
            .onAppear { recovery.loadOnce() }
            .alert("保存工作区路径与会话名称？", isPresented: $confirmingEnable) {
                Button("取消", role: .cancel) {}
                Button("允许本机保存") { recovery.enable() }
            } message: { Text("这些元数据仅存于本机私有恢复文件；可以随时关闭并删除。消息、命令和终端输出不会保存。") }
            .alert("新建 Shell，而不是恢复旧进程", isPresented: Binding(get: { reopening != nil }, set: { if !$0 { reopening = nil } })) {
                Button("取消", role: .cancel) { reopening = nil }
                Button("在该目录新建") { if let record = reopening { recovery.newShell(for: record) }; reopening = nil }
            } message: { Text(reopening?.worktree ?? "") }
    }
}
