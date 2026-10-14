import AppKit
import BerthAgent
import BerthTerminal
import SwiftUI

/// Routes the session bridge. Enter writes a line to the shell PTY. ⌘↩ writes a
/// line to the selected agent PTY. ⇧⌘↩ asks the existing planner for a new PTY.
/// Slash tokens are commands, not shell text and not an agent prompt.
enum SessionComposerModel {
    struct Command: Equatable, Identifiable {
        var id: String { token }
        let token: String
        let title: String
        let detail: String
        var slug: String { String(token.dropFirst()) }
    }

    static let commands: [Command] = [
        Command(token: "/agent-select", title: "选择默认 Agent", detail: "Codex、Claude Code 等"),
        Command(token: "/agent-new", title: "新建 Agent 会话", detail: "等同 ⇧⌘↩，可选跟一段初始任务"),
        Command(token: "/agent", title: "选择续接目标", detail: "按标题或 provider 片段，如 /agent codex"),
        Command(token: "/shell-new", title: "新建 Shell", detail: "在当前 worktree 再开一个 Shell"),
        Command(token: "/open", title: "聚焦终端", detail: "把键盘交给当前主画布会话（TUI / vim）"),
        Command(token: "/rename", title: "重命名会话", detail: "重命名当前主画布会话"),
        Command(token: "/stop", title: "结束会话", detail: "结束当前主画布会话（需确认）"),
    ]

    static let providers: [(id: String, name: String)] = [
        ("codex", "Codex"),
        ("claude", "Claude Code"),
        ("opencode", "OpenCode"),
        ("deepseek", "DeepSeek"),
        ("pi", "Pi"),
    ]

    static var knownProviderIDs: Set<String> { Set(providers.map(\.id)) }

    static func providerName(_ id: String) -> String {
        providers.first { $0.id == id }?.name ?? id
    }

    enum Chord: Equatable { case plain, command, commandShift }

    enum Route: Equatable {
        case shell(String)
        case resume(String)
        case newAgent(String)
        case openProviders
        case selectProvider(String)
        case needProvider
        case pickResumeAgent(String)
        case shellNew
        case renameSession(String)
        case stopSession
        case focusTerminal
        case none
    }

    static func matches(_ text: String) -> [Command] {
        let parts = split(text)
        guard parts.token.hasPrefix("/") else { return [] }
        if parts.token == "/" { return commands }
        let prefix = filteredCommands(for: parts.token)
        guard parts.token.count > 1, !prefix.isEmpty else { return prefix }
        let query = parts.token.dropFirst().lowercased()
        return prefix.filter {
            $0.token.dropFirst().lowercased().contains(query)
                || $0.title.lowercased().contains(query)
                || $0.detail.lowercased().contains(query)
        }
    }

    private static func filteredCommands(for token: String) -> [Command] {
        guard token != "/" else { return commands }
        return commands.filter { $0.token.hasPrefix(token) }
    }

    static func route(text: String, chord: Chord, providerChosen: Bool, resumeReady: Bool) -> Route {
        let parts = split(text)
        if let command = commands.first(where: { $0.token == parts.token }) {
            return commandRoute(command.token, argument: parts.argument, chord: chord, providerChosen: providerChosen)
        }
        if parts.token.hasPrefix("/"), chord == .plain, !matches(text).isEmpty || parts.token == "/" {
            return .none
        }
        let body = text.trimmingCharacters(in: .whitespacesAndNewlines)
        switch chord {
        case .plain:
            return body.isEmpty ? .none : .shell(body)
        case .command:
            if body.isEmpty { return .none }
            if resumeReady { return .resume(body) }
            guard providerChosen else { return .needProvider }
            return .newAgent(body)
        case .commandShift:
            if !providerChosen { return .needProvider }
            return .newAgent(body)
        }
    }

    private static func commandRoute(_ token: String, argument: String, chord: Chord, providerChosen: Bool) -> Route {
        switch token {
        case "/agent-select":
            if chord == .commandShift { return providerChosen ? .newAgent("") : .needProvider }
            return argument.isEmpty ? .openProviders : .selectProvider(argument)
        case "/agent-new":
            guard providerChosen else { return .needProvider }
            return .newAgent(argument)
        case "/agent":
            return argument.isEmpty ? .none : .pickResumeAgent(argument)
        case "/shell-new":
            return .shellNew
        case "/open":
            return .focusTerminal
        case "/rename":
            return argument.isEmpty ? .none : .renameSession(argument)
        case "/stop":
            return .stopSession
        default:
            return .none
        }
    }

    private static func split(_ text: String) -> (token: String, argument: String) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        let parts = trimmed.split(separator: " ", maxSplits: 1, omittingEmptySubsequences: true)
        let token = parts.first.map(String.init) ?? ""
        let argument = parts.count > 1 ? String(parts[1]).trimmingCharacters(in: .whitespacesAndNewlines) : ""
        return (token, argument)
    }
}

struct ProviderChoice: Identifiable, Equatable {
    let id: String
    let name: String
    /// Nil until `agent list` reports this id.
    let installed: Bool?
}

enum SessionProviderChoices {
    static func merge(_ catalogue: [ExternalAgent]) -> [ProviderChoice] {
        let known = Set(SessionComposerModel.providers.map(\.id))
        var rows = SessionComposerModel.providers.map { item -> ProviderChoice in
            let live = catalogue.first { $0.id == item.id }
            return ProviderChoice(id: item.id, name: live?.name ?? item.name, installed: live?.installed)
        }
        for agent in catalogue where !known.contains(agent.id) {
            rows.append(ProviderChoice(id: agent.id, name: agent.name, installed: agent.installed))
        }
        return rows
    }
}

/// The bridge field. Plain Return, ⌘↩ and ⇧⌘↩ never insert a newline.
/// Copy, paste, select-all and undo stay with the text view. Marked text is not submitted.
final class SessionBridgeTextView: NSTextView {
    var onShell: (() -> Void)?
    var onResume: (() -> Void)?
    var onNew: (() -> Void)?
    var onEscape: (() -> Void)?
    var onMenuStep: ((Int) -> Void)?
    var onMenuActivate: (() -> Void)?
    var onHistoryBack: (() -> Void)?
    var onCommandHeld: ((Bool) -> Void)?
    var slashMenuOpen = false

    enum Action: Equatable { case shell, resume, newAgent, escape }

    static func action(keyCode: UInt16, modifiers: NSEvent.ModifierFlags, composing: Bool, editable: Bool) -> Action? {
        guard editable, !composing else { return nil }
        let keys = modifiers.intersection([.command, .shift, .control, .option])
        if keyCode == 53, keys.isEmpty { return .escape }
        guard keyCode == 36 || keyCode == 76 else { return nil }
        if keys == [.command, .shift] { return .newAgent }
        if keys == .command { return .resume }
        if keys.isEmpty { return .shell }
        return nil
    }

    private func handle(_ event: NSEvent) -> Bool {
        guard window?.firstResponder === self, isEditable else { return false }
        let keys = event.modifierFlags.intersection([.command, .shift, .control, .option])
        if keys.isEmpty, !hasMarkedText() {
            if slashMenuOpen {
                if event.keyCode == 126 { onMenuStep?(-1); return true }
                if event.keyCode == 125 { onMenuStep?(1); return true }
                if event.keyCode == 36 || event.keyCode == 76 { onMenuActivate?(); return true }
            } else if event.keyCode == 126 {
                onHistoryBack?()
                return true
            }
        }
        guard let action = Self.action(keyCode: event.keyCode, modifiers: event.modifierFlags,
                                       composing: hasMarkedText(), editable: isEditable) else { return false }
        switch action {
        case .shell: onShell?()
        case .resume: onResume?()
        case .newAgent: onNew?()
        case .escape: onEscape?()
        }
        return true
    }

    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        if handle(event) { return true }
        return super.performKeyEquivalent(with: event)
    }

    override func keyDown(with event: NSEvent) {
        onCommandHeld?(event.modifierFlags.contains(.command))
        if !handle(event) { super.keyDown(with: event) }
    }

    override func flagsChanged(with event: NSEvent) {
        onCommandHeld?(event.modifierFlags.contains(.command))
        super.flagsChanged(with: event)
    }
}

struct SessionBridgeField: NSViewRepresentable {
    @Binding var text: String
    var enabled: Bool
    var focusToken: Int
    var slashMenuOpen: Bool
    var onShell: () -> Void
    var onResume: () -> Void
    var onNew: () -> Void
    var onEscape: () -> Void
    var onMenuStep: (Int) -> Void
    var onMenuActivate: () -> Void
    var onHistoryBack: () -> Void
    var onCommandHeld: (Bool) -> Void

    func makeCoordinator() -> Coordinator { Coordinator(text: $text) }
    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSScrollView()
        scroll.drawsBackground = false
        scroll.hasVerticalScroller = false
        scroll.autohidesScrollers = true
        let editor = SessionBridgeTextView(frame: scroll.bounds)
        editor.isRichText = false
        editor.allowsUndo = true
        editor.isAutomaticQuoteSubstitutionEnabled = false
        editor.isAutomaticDashSubstitutionEnabled = false
        editor.isAutomaticTextReplacementEnabled = false
        editor.drawsBackground = false
        editor.textContainerInset = NSSize(width: 2, height: 2)
        editor.isVerticallyResizable = true
        editor.isHorizontallyResizable = false
        editor.autoresizingMask = [.width]
        editor.textContainer?.widthTracksTextView = true
        editor.delegate = context.coordinator
        editor.setAccessibilityIdentifier("console.sessionComposer")
        editor.setAccessibilityLabel("会话输入。Enter 发送到 Shell，⌘↩ 续接选中的 Agent，⇧⌘↩ 新建 Agent")
        scroll.documentView = editor
        update(editor, context: context)
        return scroll
    }

    func updateNSView(_ scroll: NSScrollView, context: Context) {
        guard let editor = scroll.documentView as? SessionBridgeTextView else { return }
        update(editor, context: context)
    }

    private func update(_ editor: SessionBridgeTextView, context: Context) {
        context.coordinator.text = $text
        if editor.string != text && !editor.hasMarkedText() { editor.string = text }
        editor.isEditable = enabled
        editor.font = Face.nativeMono(12)
        editor.textColor = NSColor(Ink.ink)
        editor.insertionPointColor = NSColor(Ink.ink)
        editor.onShell = onShell
        editor.onResume = onResume
        editor.onNew = onNew
        editor.onEscape = onEscape
        editor.slashMenuOpen = slashMenuOpen
        editor.onMenuStep = onMenuStep
        editor.onMenuActivate = onMenuActivate
        editor.onHistoryBack = onHistoryBack
        editor.onCommandHeld = onCommandHeld
        // A recreated text view must not repeat an old focus request, or it
        // steals the terminal when the status column changes selection.
        if context.coordinator.appliedFocus != focusToken {
            context.coordinator.appliedFocus = focusToken
            if focusToken > 0 { editor.window?.makeFirstResponder(editor) }
        }
    }

    final class Coordinator: NSObject, NSTextViewDelegate {
        var text: Binding<String>
        var appliedFocus = 0
        init(text: Binding<String>) { self.text = text }
        func textDidChange(_ notification: Notification) {
            guard let editor = notification.object as? NSTextView else { return }
            text.wrappedValue = editor.string
        }
    }
}

struct SessionComposerBar: View {
    @Binding var draft: String
    var notice: String
    var shellTarget: String
    var agentTarget: String
    var agentTargetEmphasis: Bool
    var providerLabel: String
    var providerChosen: Bool
    var commands: [SessionComposerModel.Command]
    var slashHighlight: Int
    var providers: [ProviderChoice]
    var pickingProvider: Bool
    var enabled: Bool
    var focusToken: Int
    var onShell: () -> Void
    var onResume: () -> Void
    var onNew: () -> Void
    var onEscape: () -> Void
    var onMenuStep: (Int) -> Void
    var onMenuActivate: () -> Void
    var onHistoryBack: () -> Void
    var onCommandHeld: (Bool) -> Void
    var onCommand: (SessionComposerModel.Command) -> Void
    var onProvider: (String) -> Void
    var onPickProvider: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            field
            hint
        }
        .background(Ink.surface)
        .overlay(alignment: .bottomLeading) { menu }
    }

    private var field: some View {
        HStack(spacing: Metrics.inline) {
            Image(systemName: "chevron.right").font(.system(size: 10, weight: .semibold)).foregroundStyle(Ink.inkFaint)
            ZStack(alignment: .leading) {
                if draft.isEmpty {
                    Text("Shell 命令 · / 快捷操作").font(Face.mono(12)).foregroundStyle(Ink.inkFaint)
                        .padding(.leading, 6).allowsHitTesting(false)
                }
                SessionBridgeField(text: $draft, enabled: enabled, focusToken: focusToken,
                                   slashMenuOpen: pickingProvider || !commands.isEmpty,
                                   onShell: onShell, onResume: onResume, onNew: onNew, onEscape: onEscape,
                                   onMenuStep: onMenuStep, onMenuActivate: onMenuActivate, onHistoryBack: onHistoryBack,
                                   onCommandHeld: onCommandHeld)
            }
            .frame(minHeight: 32, maxHeight: 72)
            VStack(alignment: .trailing, spacing: 2) {
                Text("→ \(shellTarget)").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted).lineLimit(1)
                Text(agentTarget).font(Face.mono(Metrics.caption, agentTargetEmphasis ? .semibold : .medium))
                    .foregroundStyle(agentTargetEmphasis ? Ink.accent : (providerChosen ? Ink.inkMuted : Ink.inkFaint))
                    .padding(.horizontal, agentTargetEmphasis ? 4 : 0)
                    .background(agentTargetEmphasis ? Ink.accentSoft : Color.clear)
                    .clipShape(RoundedRectangle(cornerRadius: 4))
                    .lineLimit(1)
            }
            ConsoleToolbarAction(title: providerChosen ? providerLabel : "选择 Agent",
                                 identifier: "console.agentChip", symbol: "sparkle", perform: onPickProvider)
                .fixedSize().frame(height: 24)
                .help(providerChosen ? "默认 Agent：\(providerLabel)。点此更换，或输入 /agent-select" : "还没有默认 Agent。点此选择，或输入 /agent-select")
        }
        .padding(.horizontal, Metrics.gutter).padding(.top, 6)
    }

    private var hint: some View {
        HStack(spacing: Metrics.inline) {
            Text(notice.isEmpty ? "Enter → Shell · ⌘↩ → Agent · ⇧⌘↩ 新建 · / 命令" : notice)
                .font(Face.sans(Metrics.caption)).foregroundStyle(notice.isEmpty ? Ink.inkFaint : Ink.inkMuted)
                .lineLimit(2)
            Spacer(minLength: 0)
            ConsoleValueMarker(identifier: "console.composerHint", value: notice)
                .frame(width: 1, height: 1)
        }
        .padding(.horizontal, Metrics.gutter).padding(.vertical, 4)
    }

    @ViewBuilder private var menu: some View {
        if pickingProvider || !commands.isEmpty {
            VStack(alignment: .leading, spacing: 2) {
                if pickingProvider {
                    Text("选择默认 Agent").font(Face.mono(Metrics.caption, .semibold)).foregroundStyle(Ink.inkFaint)
                        .padding(.horizontal, 8).padding(.top, 6)
                    ForEach(providers) { provider in
                        providerRow(provider)
                    }
                } else {
                    Text("命令").font(Face.mono(Metrics.caption, .semibold)).foregroundStyle(Ink.inkFaint)
                        .padding(.horizontal, 8).padding(.top, 6)
                    ForEach(Array(commands.enumerated()), id: \.element.id) { index, command in
                        ConsoleToolbarAction(title: command.token, identifier: "console.slash." + command.slug, perform: { onCommand(command) })
                            .frame(maxWidth: .infinity, minHeight: 22, alignment: .leading)
                            .background(index == slashHighlight ? Ink.accentSoft : Color.clear)
                            .clipShape(RoundedRectangle(cornerRadius: 4))
                            .help(command.title + " · " + command.detail)
                        Text(command.detail).font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkFaint)
                            .padding(.leading, 8).padding(.bottom, 2)
                    }
                }
            }
            .padding(6)
            .frame(width: 280, alignment: .leading)
            .background(Ink.canvas)
            .clipShape(RoundedRectangle(cornerRadius: 8))
            .overlay(RoundedRectangle(cornerRadius: 8).stroke(Ink.lineStrong, lineWidth: 1))
            .shadow(color: .black.opacity(0.16), radius: 12, y: 4)
            .padding(.leading, Metrics.gutter)
            .offset(y: -52)
            .background(ConsoleValueMarker(identifier: pickingProvider ? "console.providerMenu" : "console.slashMenu", value: ""))
        }
    }

    private func providerRow(_ provider: ProviderChoice) -> some View {
        let unavailable = provider.installed == false
        let suffix = provider.installed == false ? " · 未安装" : (provider.installed == nil ? "" : "")
        return ConsoleToolbarAction(title: provider.name + suffix,
                                    identifier: "console.provider." + provider.id,
                                    perform: { onProvider(provider.id) })
            .frame(maxWidth: .infinity, minHeight: 22, alignment: .leading)
            .disabled(unavailable)
            .help(unavailable ? "未安装，不会自动安装" : "设为默认 Agent")
    }
}

struct AgentStatusColumn: View {
    let sessions: [TerminalSession]
    let resumeID: UUID?
    let previewID: UUID?
    let onPreview: (TerminalSession) -> Void
    let onEnter: (TerminalSession) -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack {
                Text("AGENT").font(Face.mono(Metrics.caption, .semibold)).foregroundStyle(Ink.inkFaint)
                Spacer(minLength: 0)
            }
            .padding(.horizontal, Metrics.gutter).padding(.vertical, 7)
            Hairline()
            if sessions.isEmpty {
                Text("还没有 Agent。\n⇧⌘↩ 在当前标签新建。")
                    .font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkMuted)
                    .padding(Metrics.gutter)
                    .fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 0)
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: 4) {
                        ForEach(sessions) { session in
                            statusRow(session)
                        }
                    }
                    .padding(6)
                }
            }
        }
        .frame(maxHeight: .infinity, alignment: .top)
        .background(Ink.surface)
        .accessibilityElement(children: .contain)
        .accessibilityLabel("Agent 状态")
    }

    private func statusRow(_ session: TerminalSession) -> some View {
        let selected = session.id == resumeID
        let expanded = session.id == previewID
        let provider = session.kind.split(separator: ":").dropFirst().first.map(String.init) ?? ""
        let inferred = session.inferredAgentState.label
        return VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 4) {
                if session.needsAttention {
                    Circle().fill(Ink.live).frame(width: 6, height: 6)
                }
                ConsoleToolbarAction(title: session.title, identifier: "console.agentStatus." + session.id.uuidString,
                                     symbol: selected ? "largecircle.fill.circle" : (session.isActive ? "circle.fill" : "circle"),
                                     perform: { onPreview(session) })
                    .frame(maxWidth: .infinity, minHeight: Metrics.hit, alignment: .leading)
                ConsoleToolbarAction(title: "进入", identifier: "console.agentEnter." + session.id.uuidString,
                                     symbol: "arrow.up.right", perform: { onEnter(session) })
                    .fixedSize().frame(height: Metrics.hit)
            }
            Text((provider.isEmpty ? "" : provider + " · ") + inferred + (selected ? " · 续接" : ""))
                .font(Face.mono(Metrics.caption)).foregroundStyle(selected ? Ink.accent : Ink.inkMuted)
                .lineLimit(1).padding(.leading, 4)
            if expanded {
                VStack(alignment: .leading, spacing: 2) {
                    ForEach(AgentActivityInference.previewLines(session.activity.outputTail), id: \.self) { line in
                        Text(line).font(Face.mono(9)).foregroundStyle(Ink.inkFaint).lineLimit(1)
                    }
                }
                .padding(4)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Ink.canvas)
                .clipShape(RoundedRectangle(cornerRadius: 4))
            }
        }
        .padding(4)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(selected ? Ink.accentSoft : Color.clear)
        .clipShape(RoundedRectangle(cornerRadius: 6))
        .help("单击设为 ⌘↩ 目标并预览；「进入」切到终端")
    }
}

struct SessionComposerFrozen: View {
    var slash: Bool
    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            if slash {
                VStack(alignment: .leading, spacing: 3) {
                    Text("/agent-select").font(Face.mono(Metrics.chrome, .medium))
                    Text("选择默认 Agent").font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkFaint)
                    Text("/agent-new").font(Face.mono(Metrics.chrome))
                    Text("新建 Agent 会话").font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkFaint)
                }
                .padding(8)
                .frame(width: 220, alignment: .leading)
                .background(Ink.canvas)
                .overlay(RoundedRectangle(cornerRadius: 8).stroke(Ink.lineStrong, lineWidth: 1))
                .padding(.leading, Metrics.gutter)
            }
            HStack(spacing: 8) {
                Text(slash ? "/" : "输入命令，或 /").font(Face.mono(12)).foregroundStyle(slash ? Ink.ink : Ink.inkFaint)
                Spacer(minLength: 0)
                Text(slash ? "选择 Agent" : "未选择")
                    .font(Face.sans(Metrics.chrome, .medium))
                    .foregroundStyle(Ink.inkMuted)
                    .padding(.horizontal, 8).padding(.vertical, 3)
                    .background(Ink.canvas)
                    .clipShape(RoundedRectangle(cornerRadius: 6))
            }
            Text("Enter Shell · ⌘↩ 续接 Agent · ⇧⌘↩ 新建")
                .font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkFaint)
        }
        .padding(.horizontal, Metrics.gutter).padding(.vertical, 6)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Ink.surface)
    }
}

struct SessionStatusFrozen: View {
    var active: Bool
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("AGENT").font(Face.mono(Metrics.caption, .semibold)).foregroundStyle(Ink.inkFaint)
            if active {
                HStack(spacing: 6) {
                    Circle().fill(Ink.live).frame(width: 6, height: 6)
                    VStack(alignment: .leading, spacing: 1) {
                        Text("Codex 1").font(Face.sans(Metrics.chrome, .semibold))
                        Text("codex · 运行中").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted)
                    }
                }
                .padding(6).frame(maxWidth: .infinity, alignment: .leading)
                .background(Ink.accentSoft).clipShape(RoundedRectangle(cornerRadius: 6))
            } else {
                Text("还没有 Agent。").font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkMuted)
            }
            Spacer(minLength: 0)
        }
        .padding(Metrics.gutter)
        .frame(width: 168, alignment: .topLeading)
        .background(Ink.surface)
    }
}

struct ConsoleValueMarker: NSViewRepresentable {
    let identifier: String
    let value: String
    func makeNSView(context: Context) -> NSView {
        let view = NSView(frame: .zero)
        view.setAccessibilityElement(true)
        return view
    }
    func updateNSView(_ view: NSView, context: Context) {
        view.setAccessibilityIdentifier(identifier)
        view.setAccessibilityValue(value)
    }
}
