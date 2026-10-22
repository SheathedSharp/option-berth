import AppKit
import SwiftTerm
import SwiftUI

/// A client-owned PTY, not a service run or an alternative daemon ledger.
/// Its starting worktree is immutable, even when the host UI selects another project.
@MainActor
public final class TerminalSession: ObservableObject, Identifiable {
    public let id = UUID()
    public let worktree: String
    @Published public private(set) var title: String
    public let kind: String
    public let terminal: HostedTerminalView
    @Published public private(set) var state = "未启动 / Not started"
    @Published public private(set) var isActive = false
    @Published public private(set) var isStopping = false
    public var onChange: (() -> Void)?
    @Published public private(set) var commandBlocks: [CommandBlock] = []
    @Published public private(set) var activity = SessionActivitySnapshot()
    @Published public private(set) var needsAttention = false
    private var commandParser: CommandBlockParser?
    private var shellLease: ShellIntegrationLease?
    private var commandOutputStartRows: [UUID: Int] = [:]
    /// Bytes received while a command block is running (OSC C→D). Preferred over
    /// buffer-row capture, which breaks when the idle keepalive PTY resizes.
    private var commandOutputAccumulators: [UUID: String] = [:]

    public init(worktree: String, title: String = "Terminal", kind: String = "terminal") {
        self.worktree = worktree.hasPrefix("/") && !worktree.utf8.contains(0) ? URL(fileURLWithPath: worktree).standardizedFileURL.resolvingSymlinksInPath().path : ""
        self.title = title
        self.kind = kind
        terminal = HostedTerminalView(frame: NSRect(x: 0, y: 0, width: 760, height: 340))
        terminal.onExit = { [weak self] status in
            guard let self else { return }
            commandParser?.interrupt()
            syncCommandBlocksFromParser()
            shellLease = nil
            isActive = false
            isStopping = false
            if let status {
                let signal = status & 0x7f
                state = signal == 0 ? "已退出 / Exit \((status >> 8) & 0xff)" : "信号退出 / Signal \(signal)"
            } else {
                state = "退出结果未知 / Exit unknown"
            }
            onChange?()
        }
        terminal.onProtocolBytes = { [weak self] bytes in
            guard let self, commandParser != nil else { return }
            // Accumulate before consume so the D-marker packet's trailing output
            // is captured before the block flips to completed.
            appendRunningCommandOutput(bytes)
            let revision = commandParser?.revision
            commandParser?.consume(bytes)
            if commandParser?.revision != revision {
                syncCommandBlocksFromParser()
            }
        }
        terminal.onBell = { [weak self] in
            guard let self else { return }
            activity.lastBellAt = Date()
            needsAttention = true
            onChange?()
            objectWillChange.send()
        }
        terminal.onBytes = { [weak self] bytes in
            guard let self else { return }
            activity.lastOutputAt = Date()
            if bytes.contains(7) {
                activity.lastBellAt = Date()
                needsAttention = true
            }
            activity.outputTail = AgentActivityInference.appendTail(activity.outputTail, bytes: bytes)
            activity.alternateScreen = terminal.getTerminal().isCurrentBufferAlternate
            onChange?()
            objectWillChange.send()
        }
        let priorProtocol = terminal.onProtocolBytes
        terminal.onProtocolBytes = { [weak self] bytes in
            priorProtocol?(bytes)
            guard let self else { return }
            if Self.containsNotify(bytes) {
                activity.lastNotifyAt = Date()
                needsAttention = true
                objectWillChange.send()
            }
        }
    }

    public func clearAttention() {
        needsAttention = false
        objectWillChange.send()
    }

    private static func containsNotify(_ bytes: ArraySlice<UInt8>) -> Bool {
        // Warp/iTerm-style OSC 777 notify and ConEmu-style OSC 9;4 progress hooks.
        let markers = ["777;notify", "9;4;"]
        let text = String(decoding: bytes, as: UTF8.self)
        return markers.contains { text.contains($0) }
    }

    public var isTaskAgent: Bool { kind.hasSuffix(":task") }

    public var acceptsComposerTurn: Bool { isActive && !isTaskAgent }

    public var inferredAgentState: AgentInferredState {
        AgentActivityInference.infer(isActive: isActive, isTaskMode: isTaskAgent, snapshot: activity)
    }

    public var shellIntegrationEnabled: Bool { commandParser != nil }

    public var shellForegroundBusy: Bool {
        guard kind == "terminal", isActive else { return false }
        if activity.alternateScreen { return true }
        return commandBlocks.last?.isRunning == true
    }

    public func clearCommandHistory() {
        commandParser?.clear()
        commandOutputStartRows.removeAll()
        commandOutputAccumulators.removeAll()
        commandBlocks = []
        onChange?()
    }

    private func syncCommandBlocksFromParser() {
        guard let parser = commandParser else {
            commandBlocks = []
            return
        }
        let prior = Dictionary(uniqueKeysWithValues: commandBlocks.map { ($0.id, $0.capturedOutput) })
        var blocks = parser.blocks
        let terminal = terminal.getTerminal()
        for index in blocks.indices {
            var block = blocks[index]
            if let saved = prior[block.id], block.capturedOutput == nil {
                block.capturedOutput = saved
            }
            if block.isRunning {
                if commandOutputAccumulators[block.id] == nil {
                    commandOutputAccumulators[block.id] = ""
                }
                if commandOutputStartRows[block.id] == nil {
                    commandOutputStartRows[block.id] = ShellCommandCapture.absoluteCursorRow(terminal: terminal)
                }
            } else if block.capturedOutput == nil {
                if let acc = commandOutputAccumulators.removeValue(forKey: block.id) {
                    let finalized = ShellCommandCapture.finalizeAccumulated(acc)
                    if !finalized.isEmpty {
                        block.capturedOutput = finalized
                    }
                }
                if block.capturedOutput == nil,
                   let start = commandOutputStartRows.removeValue(forKey: block.id) {
                    block.capturedOutput = ShellCommandCapture.capture(terminal: terminal, startRow: start)
                } else {
                    _ = commandOutputStartRows.removeValue(forKey: block.id)
                }
            }
            blocks[index] = block
        }
        let live = Set(blocks.map(\.id))
        commandOutputStartRows = commandOutputStartRows.filter { live.contains($0.key) }
        commandOutputAccumulators = commandOutputAccumulators.filter { live.contains($0.key) }
        commandBlocks = blocks
        onChange?()
    }

    /// Append PTY payload to the running command's accumulator (OSC already stripped).
    private func appendRunningCommandOutput(_ bytes: ArraySlice<UInt8>) {
        guard let id = commandBlocks.last(where: { $0.isRunning })?.id,
              commandOutputAccumulators[id] != nil else { return }
        let chunk = ShellCommandCapture.stripOSC(String(decoding: bytes, as: UTF8.self))
        guard !chunk.isEmpty else { return }
        commandOutputAccumulators[id, default: ""] += chunk
    }

    public func start(executable: String, arguments: [String], environment: [String: String]? = nil, shellIntegration: Bool = false) throws {
        guard !isActive, terminal.process == nil else { throw TerminalFailure("此会话不能重复启动 / Session already used") }
        var isDirectory: ObjCBool = false
        guard worktree.hasPrefix("/"), FileManager.default.fileExists(atPath: worktree, isDirectory: &isDirectory), isDirectory.boolValue else {
            throw TerminalFailure("工作目录不存在 / Worktree directory is unavailable")
        }
        guard executable.hasPrefix("/"), !executable.utf8.contains(0), FileManager.default.isExecutableFile(atPath: executable),
              !arguments.contains(where: { $0.utf8.contains(0) }) else {
            throw TerminalFailure("可执行文件或参数无效 / Invalid executable or arguments")
        }
        var env = environment ?? Self.environment()
        guard env.allSatisfy({ !$0.key.isEmpty && !$0.key.contains("=") && !$0.key.utf8.contains(0) && !$0.value.utf8.contains(0) }) else {
            throw TerminalFailure("终端环境变量无效 / Invalid terminal environment")
        }
        var launchArguments = arguments
        if shellIntegration {
            guard kind == "terminal", ShellIntegrationLease.supports(executable: executable) else {
                throw TerminalFailure("命令块集成仅支持交互式 zsh、bash 或 fish / Shell integration requires interactive zsh, bash, or fish")
            }
            let lease = try ShellIntegrationLease(executable: executable, environment: env)
            shellLease = lease
            commandParser = CommandBlockParser(nonce: lease.nonce)
            terminal.getTerminal().registerOscHandler(code: 633) { _ in /* Host parser consumes command metadata. */ }
            env = lease.environment
            launchArguments = lease.launchArguments
        }
        env["PWD"] = worktree
        env["TERM"] = "xterm-256color"
        env["COLORTERM"] = "truecolor"
        isActive = true
        state = "运行中 / Running"
        terminal.launch(executable: executable, arguments: launchArguments, environment: env, directory: worktree)
        if terminal.process?.shellPid == 0 {
            isActive = false
            shellLease = nil
            state = "启动失败 / Launch failed"
            throw TerminalFailure(state)
        }
        onChange?()
    }

    public func rename(_ name: String) throws {
        let value = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !value.isEmpty, value.count <= 64,
              value.unicodeScalars.allSatisfy({ !CharacterSet.controlCharacters.contains($0) }) else {
            throw TerminalFailure("会话名称须为 1–64 字且不含控制字符 / Use 1–64 characters without control codes")
        }
        title = value
        onChange?()
    }

    public func stop(force: Bool = false) {
        guard isActive, (!isStopping || force), let process = terminal.process, process.shellPid > 0 else { return }
        // Signal only our unreaped direct child. Closing a shell should not become
        // a broad kill-by-name or an implicit `oberth down` for project services.
        if Darwin.kill(process.shellPid, force ? SIGKILL : SIGHUP) == 0 {
            isStopping = true
            state = "等待退出 / Waiting for exit"
        } else {
            state = "无法请求退出 / Stop request failed"
        }
        // Keep the view/process retained until the library's reaper reports exit.
        // LocalProcess.terminate() cancels that monitor, so it is not used here.
        onChange?()
    }

    public static func environment() -> [String: String] {
        var env = ProcessInfo.processInfo.environment
        let home = FileManager.default.homeDirectoryForCurrentUser.path
        var paths = (env["PATH"] ?? "").split(separator: ":").map(String.init).filter { $0.hasPrefix("/") }
        for path in [home + "/.local/bin", "/opt/homebrew/bin", "/usr/local/bin", "/usr/bin", "/bin", "/usr/sbin", "/sbin"] where !paths.contains(path) {
            paths.append(path)
        }
        env["PATH"] = paths.joined(separator: ":")
        return env
    }

    public static var shell: String {
        let candidate = ProcessInfo.processInfo.environment["SHELL"] ?? "/bin/zsh"
        return candidate.hasPrefix("/") && FileManager.default.isExecutableFile(atPath: candidate) ? candidate : "/bin/zsh"
    }
}

public struct TerminalFailure: LocalizedError {
    let message: String
    public init(_ message: String) { self.message = message }
    public var errorDescription: String? { message }
}

/// SwiftTerm owns emulation and PTY transport. This delegate supplies the host's
/// security policy: OSC data cannot read/write the clipboard or retarget a session.
public final class HostedTerminalView: TerminalView, TerminalViewDelegate, LocalProcessDelegate {
    public private(set) var process: LocalProcess?
    public var onExit: ((Int32?) -> Void)?
    public var onBytes: ((ArraySlice<UInt8>) -> Void)?
    /// Test hook: bytes written to the PTY (not echoed child output).
    public var onOutboundBytes: ((ArraySlice<UInt8>) -> Void)?
    var onProtocolBytes: ((ArraySlice<UInt8>) -> Void)?
    var onBell: (() -> Void)?
    public override init(frame: CGRect) {
        super.init(frame: frame, font: NSFont.monospacedSystemFont(ofSize: 12, weight: .regular),
                   options: TerminalOptions(cols: 96, rows: 24, cursorStyle: .steadyBar,
                                            scrollback: 5000, enableSixelReported: false,
                                            kittyImageCacheLimitBytes: 16 * 1024 * 1024))
        terminalDelegate = self
        nativeBackgroundColor = NSColor.textBackgroundColor
        nativeForegroundColor = NSColor.labelColor
    }
    public required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }

    public func launch(executable: String, arguments: [String], environment: [String: String], directory: String) {
        let child = LocalProcess(delegate: self)
        process = child
        child.startProcess(executable: executable, args: arguments,
                           environment: environment.sorted { $0.key < $1.key }.map { "\($0.key)=\($0.value)" },
                           currentDirectory: directory)
    }

    public func processTerminated(_ source: LocalProcess, exitCode: Int32?) { onExit?(exitCode) }
    public func dataReceived(slice: ArraySlice<UInt8>) { feed(byteArray: slice); onProtocolBytes?(slice); onBytes?(slice) }
    public func bell(source: TerminalView) { onBell?() }
    public func iTermContent(source: TerminalView, content: ArraySlice<UInt8>) {}
    public func getWindowSize() -> winsize {
        winsize(ws_row: UInt16(clamping: terminal.rows), ws_col: UInt16(clamping: terminal.cols), ws_xpixel: 0, ws_ypixel: 0)
    }
    public func sizeChanged(source: TerminalView, newCols: Int, newRows: Int) {
        guard let process, process.running else { return }
        var size = getWindowSize()
        _ = PseudoTerminalHelpers.setWinSize(masterPtyDescriptor: process.childfd, windowSize: &size)
    }
    public func send(source: TerminalView, data: ArraySlice<UInt8>) {
        onOutboundBytes?(data)
        process?.send(data: data)
    }
    public func setTerminalTitle(source: TerminalView, title: String) { /* Untrusted OSC title is not identity. */ }
    public func hostCurrentDirectoryUpdate(source: TerminalView, directory: String?) { /* Never retarget. */ }
    public func scrolled(source: TerminalView, position: Double) {}
    public func rangeChanged(source: TerminalView, startY: Int, endY: Int) {}
    public func clipboardCopy(source: TerminalView, content: Data) { /* OSC 52 writes are denied. */ }
    public func clipboardRead(source: TerminalView) -> Data? { nil }
    public func requestOpenLink(source: TerminalView, link: String, params: [String: String]) {
        guard let url = URL(string: link), ["http", "https"].contains(url.scheme?.lowercased() ?? "") else { return }
        let alert = NSAlert()
        alert.messageText = "打开终端链接？ / Open terminal link?"
        alert.informativeText = url.absoluteString
        alert.addButton(withTitle: "取消 / Cancel")
        alert.addButton(withTitle: "打开 / Open")
        if alert.runModal() == .alertSecondButtonReturn { NSWorkspace.shared.open(url) }
    }
    public static func containing(_ responder: NSResponder?) -> HostedTerminalView? {
        var view = responder as? NSView
        while let current = view {
            if let terminal = current as? HostedTerminalView { return terminal }
            view = current.superview
        }
        return nil
    }
    public override func performKeyEquivalent(with event: NSEvent) -> Bool {
        if event.modifierFlags.intersection(.deviceIndependentFlagsMask) == .command {
            switch event.charactersIgnoringModifiers {
            case "c": copy(self); return true
            case "v": paste(self); return true
            case "a": selectAll(self); return true
            case "f":
                let item = NSMenuItem()
                item.tag = NSTextFinder.Action.showFindInterface.rawValue
                performTextFinderAction(item)
                return true
            default: break
            }
        }
        return super.performKeyEquivalent(with: event)
    }
}

