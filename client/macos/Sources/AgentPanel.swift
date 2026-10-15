import AppKit
import BerthAgent
import BerthTerminal
import SwiftUI

/// Owns only the cancellable external-agent launch plan. Live PTYs remain in the
/// sole TerminalSessions registry and are never owned by a composer/view mode.
@MainActor
final class AgentLaunchController: ObservableObject {
    @Published private(set) var providers: [ExternalAgent] = []
    private enum Phase { case idle, discovery, plan }
    @Published private var phase = Phase.idle
    var loading: Bool { phase != .idle }
    var planning: Bool { phase == .plan }
    @Published private(set) var problem: String?
    private var operation: Task<Void, Never>?
    private var generation = UUID()
    private var pending: (() -> Void)?
    deinit { operation?.cancel() }
    func provider(_ workspace: ConsoleWorkspace) -> ExternalAgent? { providers.first { $0.id == workspace.providerID } }
    func available(_ workspace: ConsoleWorkspace) -> Bool { !loading && provider(workspace)?.installed == true }
    func cancel() {
        generation = UUID(); operation?.cancel(); operation = nil; phase = .idle; pending = nil
    }

    /// Runs `body` once the provider catalogue is loaded. A chosen provider is not replaced.
    func whenReady(_ workspace: ConsoleWorkspace, _ body: @escaping () -> Void) {
        if !providers.isEmpty { body(); return }
        pending = body
        refresh(workspace)
    }

    func refresh(_ workspace: ConsoleWorkspace) {
        if loading, !providers.isEmpty { return }
        if loading { operation?.cancel(); operation = nil; phase = .idle }
        guard let binary = DaemonLaunch.binaryPath() else {
            problem = "找不到兼容的 oberth"
            pending = nil
            return
        }
        let id = UUID(); generation = id; phase = .discovery; problem = nil
        operation = Task { @MainActor in
            defer { if generation == id { phase = .idle; operation = nil } }
            do {
                let values = try await AgentBridge.providers(binary: binary, environment: TerminalSession.environment())
                try Task.checkCancellation()
                guard generation == id else { return }
                providers = values
                if !workspace.providerChosen,
                   !values.contains(where: { $0.id == workspace.providerID }),
                   let first = values.first(where: { $0.installed }) ?? values.first { workspace.providerID = first.id }
                if generation == id { phase = .idle; operation = nil }
                let follow = pending
                pending = nil
                follow?()
            } catch is CancellationError {
                if generation == id { pending = nil }
            } catch {
                if generation == id { problem = error.localizedDescription; pending = nil }
            }
        }
    }
    func launch(root: String, workspace: ConsoleWorkspace, prompt: Bool, resumeFile: String? = nil,
                didLaunch: @escaping (TerminalSession) -> Void) {
        guard available(workspace), let provider = provider(workspace), let binary = DaemonLaunch.binaryPath() else { return }
        let message = prompt ? workspace.draft : nil
        guard !prompt || !(message ?? "").trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        let mode = prompt && !provider.nativePrompt ? "task" : "native"
        let id = UUID(); generation = id; phase = .plan; problem = nil
        operation = Task { @MainActor in
            defer { if generation == id { phase = .idle; operation = nil } }
            do {
                let plan = try await AgentBridge.plan(binary: binary, provider: provider.id, root: root, mode: mode,
                    prompt: message, environment: TerminalSession.environment(), resumeFile: resumeFile)
                try Task.checkCancellation()
                guard generation == id else { return }
                let count = TerminalSessions.shared.inWorktree(root).filter { $0.kind.hasPrefix("agent:") }.count
                let title = "\(provider.name) \(count + 1)" + (resumeFile == nil ? (mode == "task" ? " · task" : "") : " · 续接")
                let session = try TerminalSessions.shared.add(worktree: plan.worktree, title: title,
                    kind: "agent:\(provider.id):\(mode)", executable: plan.executable, arguments: plan.arguments, select: false)
                if prompt && workspace.draft == message { workspace.draft = "" }
                workspace.nativeExpanded = mode == "native"
                didLaunch(session)
            } catch is CancellationError {} catch { if generation == id { problem = error.localizedDescription } }
        }
    }
}
