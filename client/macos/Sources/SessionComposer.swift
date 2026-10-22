import AppKit
import BerthAgent
import BerthTerminal
import SwiftUI

/// Composer key chords and provider display names. Routing lives in `ComposerRouter`; slash commands in `SlashParser`.
enum SessionComposerModel {
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
}

/// Explicit input mode on the unified session surface (Warp-like).
/// Layout stays one canvas + one input; mode only changes chrome and plain Enter routing.
enum ComposerInputMode: String, Equatable {
    case shell
    case agent
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
    var onSendControl: ((UInt8) -> Void)?
    var onShellTab: (() -> Void)?
    var onToggleMode: (() -> Void)?
    var shellTabEnabled = false
    var slashMenuOpen = false
    /// When true, plain Enter follows the Agent chord path (resume/send).
    var preferAgentEnter = false

    enum Action: Equatable { case shell, resume, newAgent, escape, toggleMode }

    static func action(keyCode: UInt16, modifiers: NSEvent.ModifierFlags, composing: Bool, editable: Bool,
                       preferAgentEnter: Bool = false) -> Action? {
        guard editable, !composing else { return nil }
        let keys = modifiers.intersection([.command, .shift, .control, .option])
        if keyCode == 53, keys.isEmpty { return .escape }
        // ⌘I toggles Shell ↔ Agent input mode (Warp-like). Claim it here so
        // AppKit does not treat Command-I as italics inside the text view.
        if keyCode == 34, keys == .command { return .toggleMode }
        guard keyCode == 36 || keyCode == 76 else { return nil }
        if keys == [.command, .shift] { return .newAgent }
        if keys == .command { return .resume }
        if keys.isEmpty { return preferAgentEnter ? .resume : .shell }
        return nil
    }

    private func handleControl(_ event: NSEvent) -> Bool {
        let keys = event.modifierFlags.intersection([.command, .shift, .control, .option])
        guard keys == .control else { return false }
        if event.keyCode == 8 {
            if !string.isEmpty {
                undoManager?.beginUndoGrouping()
                string = ""
                undoManager?.endUndoGrouping()
                return true
            }
            onSendControl?(0x03)
            return true
        }
        if event.keyCode == 37 {
            onSendControl?(0x0c)
            return true
        }
        return false
    }

    private func handle(_ event: NSEvent) -> Bool {
        guard window?.firstResponder === self, isEditable else { return false }
        if handleControl(event) { return true }
        let keys = event.modifierFlags.intersection([.command, .shift, .control, .option])
        if keys.isEmpty, !hasMarkedText() {
            if slashMenuOpen {
                if event.keyCode == 126 { onMenuStep?(-1); return true }
                if event.keyCode == 125 { onMenuStep?(1); return true }
                if event.keyCode == 48 || event.keyCode == 36 || event.keyCode == 76 { onMenuActivate?(); return true }
            } else if event.keyCode == 48, shellTabEnabled {
                onShellTab?()
                return true
            } else if event.keyCode == 126 {
                onHistoryBack?()
                return true
            }
        }
        guard let action = Self.action(keyCode: event.keyCode, modifiers: event.modifierFlags,
                                       composing: hasMarkedText(), editable: isEditable,
                                       preferAgentEnter: preferAgentEnter) else { return false }
        switch action {
        case .shell: onShell?()
        case .resume: onResume?()
        case .newAgent: onNew?()
        case .escape: onEscape?()
        case .toggleMode: onToggleMode?()
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
    var preferAgentEnter: Bool = false
    var onShell: () -> Void
    var onResume: () -> Void
    var onNew: () -> Void
    var onEscape: () -> Void
    var onMenuStep: (Int) -> Void
    var onMenuActivate: () -> Void
    var onHistoryBack: () -> Void
    var onCommandHeld: (Bool) -> Void
    var onSendControl: (UInt8) -> Void
    var shellTabEnabled: Bool
    var onShellTab: () -> Void
    var onToggleMode: () -> Void = {}

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
        editor.setAccessibilityLabel("会话输入。⌘I 切换 Shell/Agent。Enter 按当前模式发送，⌘↩ 交给 Agent，⇧⌘↩ 新建 Agent")
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
        editor.onSendControl = onSendControl
        editor.shellTabEnabled = shellTabEnabled
        editor.onShellTab = onShellTab
        editor.onToggleMode = onToggleMode
        editor.preferAgentEnter = preferAgentEnter
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
    var shellAttachment: ComposerShellAttachment?
    var onClearAttachment: () -> Void
    /// Integration / status hint — shown quietly on the route row, never as a second title bar.
    var statusHint: String = ""
    var inputMode: ComposerInputMode = .shell
    var providerLabel: String
    var providerChosen: Bool
    var slashCommands: [SlashCommand]
    var slashHighlight: Int
    var providers: [ProviderChoice]
    var pickingProvider: Bool
    var enabled: Bool
    var focusToken: Int
    var onShell: () -> Void
    var onResume: () -> Void
    var onNew: () -> Void
    var onEscape: () -> Void
    var onToggleMode: () -> Void = {}
    var onMenuStep: (Int) -> Void
    var onMenuActivate: () -> Void
    var onHistoryBack: () -> Void
    var onCommandHeld: (Bool) -> Void
    var onSendControl: (UInt8) -> Void
    var shellTabEnabled: Bool
    var onShellTab: () -> Void
    var onSlash: (SlashCommand) -> Void
    var onProvider: (String) -> Void
    var onPickProvider: () -> Void

    private var agentMode: Bool { inputMode == .agent }

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            if let shellAttachment {
                HStack(spacing: 6) {
                    Text("\(shellAttachment.shellTitle) · \(shellAttachment.command)")
                        .font(Face.mono(Metrics.caption))
                        .foregroundStyle(Ink.inkMuted)
                        .lineLimit(1)
                    if let code = shellAttachment.exitCode {
                        Text("exit \(code)").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkFaint)
                    }
                    Spacer(minLength: 0)
                    Button(action: onClearAttachment) {
                        Image(systemName: "xmark.circle.fill").foregroundStyle(Ink.inkFaint)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("移除 Shell 附件")
                }
                .padding(.horizontal, Metrics.gutter)
                .padding(.top, 6)
                .background(ConsoleValueMarker(identifier: "console.composerAttachment",
                                               value: shellAttachment.command))
            }
            field
            // Notices only — no multi-segment route hint spam (v15 A).
            if !notice.isEmpty || !statusHint.isEmpty {
                quietNoticeRow
            }
        }
        // Same plane as the canvas — no surface well / second card.
        .background(Ink.canvas)
        .accessibilityIdentifier("console.composerBar")
        .accessibilityValue(inputMode.rawValue)
    }

    private var field: some View {
        HStack(spacing: Metrics.inline) {
            modeMarker
            ZStack(alignment: .leading) {
                if draft.isEmpty {
                    Text(placeholder)
                        .font(Face.mono(12)).foregroundStyle(Ink.inkFaint)
                        .padding(.leading, 4).allowsHitTesting(false)
                }
                SessionBridgeField(text: $draft, enabled: enabled, focusToken: focusToken,
                                   slashMenuOpen: pickingProvider || !slashCommands.isEmpty,
                                   preferAgentEnter: agentMode,
                                   onShell: onShell, onResume: onResume, onNew: onNew, onEscape: onEscape,
                                   onMenuStep: onMenuStep, onMenuActivate: onMenuActivate, onHistoryBack: onHistoryBack,
                                   onCommandHeld: onCommandHeld,
                                   onSendControl: onSendControl, shellTabEnabled: shellTabEnabled, onShellTab: onShellTab,
                                   onToggleMode: onToggleMode)
            }
            .frame(minHeight: 28, maxHeight: 120)
            Text(compactHint)
                .font(Face.mono(Metrics.caption))
                .foregroundStyle(Ink.inkFaint)
                .lineLimit(1)
                .layoutPriority(-1)
            if agentMode {
                agentToolbelt
            }
        }
        .padding(.horizontal, Metrics.gutter)
        .padding(.top, shellAttachment == nil ? 8 : 2)
        .padding(.bottom, 8)
        .overlay(alignment: .bottomLeading) { menu }
    }

    private var modeMarker: some View {
        Button(action: onToggleMode) {
            HStack(spacing: 4) {
                Image(systemName: agentMode ? "sparkle" : "chevron.right")
                    .font(.system(size: 10, weight: .semibold))
                Text(modeChipLabel)
                    .font(Face.mono(Metrics.caption, .semibold))
            }
            .foregroundStyle(agentMode ? Ink.accent : Ink.inkMuted)
            .padding(.horizontal, 6)
            .padding(.vertical, 3)
            .background(agentMode ? Ink.accentSoft : Color.clear)
            .clipShape(RoundedRectangle(cornerRadius: 5, style: .continuous))
        }
        .buttonStyle(.plain)
        .help(agentMode ? "当前 Agent 输入 · ⌘I 切回 Shell" : "当前 Shell 输入 · ⌘I 切到 Agent")
        .accessibilityIdentifier("console.composerMode")
        .accessibilityValue(inputMode.rawValue)
    }

    private var modeChipLabel: String {
        if agentMode {
            return providerChosen ? "Agent · \(providerLabel)" : "Agent"
        }
        return "Shell"
    }

    /// One quiet inline hint — matches ASCII A (`↩ run · ⌘I Agent · /`).
    private var compactHint: String {
        if agentMode {
            return providerChosen ? "↩ send · ⌘I Shell · /" : "↩ pick Agent · ⌘I Shell · /"
        }
        return "↩ run · ⌘I Agent · /"
    }

    private var placeholder: String {
        if agentMode {
            return providerChosen ? "发给 \(providerLabel)…" : "先选择 Agent…"
        }
        return ""
    }

    private var quietNoticeRow: some View {
        HStack(spacing: Metrics.inline) {
            if !statusHint.isEmpty {
                Text(statusHint).font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkFaint).lineLimit(1)
            }
            if !notice.isEmpty {
                Text(notice).font(Face.sans(Metrics.caption)).foregroundStyle(Change.changed).lineLimit(1)
            }
            Spacer(minLength: 0)
            ConsoleValueMarker(identifier: "console.composerHint", value: notice)
                .frame(width: 1, height: 1)
        }
        .padding(.horizontal, Metrics.gutter)
        .padding(.bottom, 6)
    }

    @ViewBuilder private var agentToolbelt: some View {
        Button(action: onPickProvider) {
            Text(providerChosen ? providerLabel : "选择 Agent")
                .font(Face.sans(Metrics.caption, .medium))
                .foregroundStyle(providerChosen ? Ink.inkMuted : Change.changed)
                .padding(.horizontal, 8).padding(.vertical, 3)
                .background(Ink.sunken)
                .clipShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
        }
        .buttonStyle(.plain)
        .help("选择默认 Agent")
        .accessibilityIdentifier("console.composerProvider")
    }

    @ViewBuilder private var menu: some View {
        if pickingProvider || !slashCommands.isEmpty {
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
                    ForEach(Array(slashCommands.enumerated()), id: \.element.rawValue) { index, command in
                        ConsoleToolbarAction(title: command.rawValue, identifier: "console.slash." + command.rawValue.dropFirst(),
                                             perform: { onSlash(command) })
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
            .offset(y: -6)
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
    var streamRows: [AgentRow] = []
    let resumeID: UUID?
    var streamResumeID: UUID?
    let previewID: UUID?
    let providerLabel: String
    let providerChosen: Bool
    var providerChoices: [ProviderChoice] = []
    var defaultProviderID: String = ""
    var canOpenNativeTerminal: Bool = false
    var canResumeFromFile: Bool = false
    let onPickProvider: () -> Void
    let onNewAgent: () -> Void
    var onNewAgentProvider: (String) -> Void = { _ in }
    var onOpenNativeTerminal: () -> Void = {}
    var onResumeFromFile: () -> Void = {}
    let onSelectTerminal: (TerminalSession) -> Void
    var onStreamTarget: (UUID) -> Void = { _ in }
    var streamInlineRequest: AgentRequest?
    var onStreamRespond: (String, AgentDecision) -> Void = { _, _ in }

    private var activityCounts: (needsYou: Int, working: Int) {
        var counts = AgentRowProjector.aggregateCounts(streamRows)
        for session in sessions where session.isTaskAgent && session.isActive {
            if session.commandBlocks.last?.isRunning == true { counts.working += 1 }
        }
        return counts
    }

    private var otherAgentProviders: [ProviderChoice] {
        providerChoices.filter { $0.id != defaultProviderID && $0.installed != false }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: Metrics.inline) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Agent").font(Face.sans(Metrics.chrome, .semibold))
                    if activityCounts.needsYou > 0 || activityCounts.working > 0 {
                        HStack(spacing: 4) {
                            if activityCounts.needsYou > 0 {
                                Text("需要你 \(activityCounts.needsYou)")
                                    .foregroundStyle(Change.changed)
                            }
                            if activityCounts.needsYou > 0 && activityCounts.working > 0 {
                                Text("·").foregroundStyle(Ink.inkMuted)
                            }
                            if activityCounts.working > 0 {
                                Text("工作中 \(activityCounts.working)").foregroundStyle(Ink.inkMuted)
                            }
                        }
                        .font(Face.sans(Metrics.caption))
                    }
                }
                Spacer(minLength: 0)
                Menu {
                    Button("新建默认 Agent 会话", action: onNewAgent)
                    if !otherAgentProviders.isEmpty {
                        Menu("新建其他 Agent 会话") {
                            ForEach(otherAgentProviders) { choice in
                                Button(choice.name) { onNewAgentProvider(choice.id) }
                            }
                        }
                    }
                    Button("在原生终端中打开…", action: onOpenNativeTerminal)
                        .disabled(!canOpenNativeTerminal)
                    Button("从会话文件续接…", action: onResumeFromFile)
                        .disabled(!canResumeFromFile)
                } label: {
                    Text("＋").font(Face.sans(Metrics.chrome, .medium))
                }
                .menuStyle(.borderlessButton)
                .fixedSize()
                Button(action: onPickProvider) {
                    Text(providerChosen ? "默认 \(providerLabel) ▾" : "选择 Agent")
                        .font(Face.sans(Metrics.caption))
                        .foregroundStyle(providerChosen ? Ink.inkMuted : Change.changed)
                }
                .buttonStyle(.plain)
            }
            .padding(.horizontal, Metrics.gutter).padding(.vertical, 7)
            Hairline()
            if sessions.isEmpty && streamRows.isEmpty {
                VStack(alignment: .leading, spacing: 6) {
                    Text("还没有 Agent 会话。")
                        .font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkMuted)
                    if providerChosen {
                        Text("在下方写下任务，⌘↩ 交给 \(providerLabel)。")
                            .font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkMuted)
                    } else {
                        HStack(spacing: 8) {
                            Text("先选择默认 Agent")
                                .font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkMuted)
                            Button("选择", action: onPickProvider)
                                .font(Face.sans(Metrics.caption, .medium))
                                .buttonStyle(.plain)
                                .foregroundStyle(Ink.accent)
                        }
                    }
                }
                .padding(Metrics.gutter)
                .fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 0)
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: 4) {
                        ForEach(streamRows) { row in
                            streamStatusRow(row)
                        }
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

    private func streamStatusRow(_ row: AgentRow) -> some View {
        let selected = row.id == streamResumeID
        let showInlineApproval = row.id == previewID && row.status == .needsYou
        return VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 4) {
                ConsoleToolbarAction(title: row.title, identifier: "console.streamStatus." + row.id.uuidString,
                                     symbol: selected ? "largecircle.fill.circle" : "circle",
                                     perform: { onStreamTarget(row.id) })
                    .frame(maxWidth: .infinity, minHeight: Metrics.hit, alignment: .leading)
                if selected {
                    Text("⌘↩").font(Face.mono(9)).foregroundStyle(Ink.accent)
                }
            }
            Text((row.activity ?? "")).font(Face.mono(Metrics.caption))
                .foregroundStyle(row.status == .needsYou ? Change.changed : Ink.inkMuted).lineLimit(2)
            if showInlineApproval, let request = streamInlineRequest {
                HStack(spacing: 6) {
                    if request.decisions.contains(.allow) {
                        ConsoleToolbarAction(title: "允许",
                                             identifier: "console.streamRowApprove.\(request.id).allow",
                                             perform: { onStreamRespond(request.id, .allow) })
                    }
                    if request.decisions.contains(.deny) {
                        ConsoleToolbarAction(title: "拒绝",
                                             identifier: "console.streamRowApprove.\(request.id).deny",
                                             perform: { onStreamRespond(request.id, .deny) })
                    }
                }
            }
        }
        .padding(6)
        .background(selected ? Ink.accentSoft : Color.clear)
        .overlay(alignment: .leading) {
            if selected {
                RoundedRectangle(cornerRadius: 1.5).fill(Ink.accent).frame(width: 3)
            }
        }
        .clipShape(RoundedRectangle(cornerRadius: 4, style: .continuous))
    }

    private func statusRow(_ session: TerminalSession) -> some View {
        let selected = session.id == resumeID
        let status = ComposerConsoleBridge.agentStatusLabel(session)
        return VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 4) {
                if session.needsAttention {
                    Circle().fill(Ink.live).frame(width: 6, height: 6)
                }
                ConsoleToolbarAction(title: session.title, identifier: "console.agentStatus." + session.id.uuidString,
                                     symbol: selected ? "largecircle.fill.circle" : (session.isActive ? "circle.fill" : "circle"),
                                     perform: { onSelectTerminal(session) })
                    .frame(maxWidth: .infinity, minHeight: Metrics.hit, alignment: .leading)
                if selected {
                    Text("⌘↩").font(Face.mono(9)).foregroundStyle(Ink.accent)
                }
            }
            Text(status)
                .font(Face.mono(Metrics.caption)).foregroundStyle(selected ? Ink.accent : Ink.inkMuted)
                .lineLimit(2).padding(.leading, 4)
        }
        .padding(6)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(selected ? Ink.accentSoft : Color.clear)
        .overlay(alignment: .leading) {
            if selected {
                RoundedRectangle(cornerRadius: 1.5).fill(Ink.accent).frame(width: 3)
            }
        }
        .clipShape(RoundedRectangle(cornerRadius: 4, style: .continuous))
        .help("单击设为 ⌘↩ 目标并打开终端")
    }
}

struct SessionComposerFrozen: View {
    var slash: Bool
    var noProvider = false
    var inputMode: ComposerInputMode = .shell
    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            if slash {
                VStack(alignment: .leading, spacing: 3) {
                    Text("/agent-select").font(Face.mono(Metrics.chrome, .medium))
                    Text("选择默认 Agent").font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkFaint)
                    Text("/agent-new").font(Face.mono(Metrics.chrome))
                    Text("新建 Agent 会话").font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkFaint)
                    Text("/ob-server").font(Face.mono(Metrics.chrome))
                    Text("服务面板").font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkFaint)
                    Text("/git").font(Face.mono(Metrics.chrome))
                    Text("Git 面板").font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkFaint)
                }
                .padding(8)
                .frame(width: 220, alignment: .leading)
                .background(Ink.canvas)
                .overlay(RoundedRectangle(cornerRadius: 8).stroke(Ink.lineStrong, lineWidth: 1))
                .padding(.leading, Metrics.gutter)
            }
            // Single input row — matches ASCII A (no second hint stack).
            HStack(spacing: 8) {
                HStack(spacing: 4) {
                    Image(systemName: inputMode == .agent ? "sparkle" : "chevron.right")
                        .font(.system(size: 10, weight: .semibold))
                    Text(inputMode == .agent
                          ? (noProvider ? "Agent" : "Agent · Codex")
                          : "Shell")
                        .font(Face.mono(Metrics.caption, .semibold))
                }
                .foregroundStyle(inputMode == .agent ? Ink.accent : Ink.inkMuted)
                .padding(.horizontal, 6).padding(.vertical, 3)
                .background(inputMode == .agent ? Ink.accentSoft : Color.clear)
                .clipShape(RoundedRectangle(cornerRadius: 5, style: .continuous))
                Text(slash ? "/" : (inputMode == .agent ? "fix the flaky test…" : "git pull"))
                    .font(Face.mono(12)).foregroundStyle(slash ? Ink.ink : Ink.inkFaint)
                Spacer(minLength: 0)
                Text(inputMode == .agent
                      ? (noProvider ? "↩ pick Agent · ⌘I Shell · /" : "↩ send · ⌘I Shell · /")
                      : "↩ run · ⌘I Agent · /")
                    .font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkFaint)
                if inputMode == .agent {
                    Text(noProvider ? "选择 Agent" : "Codex")
                        .font(Face.sans(Metrics.caption, .medium))
                        .foregroundStyle(noProvider ? Change.changed : Ink.inkMuted)
                        .padding(.horizontal, 8).padding(.vertical, 3)
                        .background(Ink.sunken)
                        .clipShape(RoundedRectangle(cornerRadius: 6))
                }
            }
        }
        .padding(.horizontal, Metrics.gutter).padding(.vertical, 8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Ink.canvas)
    }
}

struct AgentStreamMixedFrozen: View {
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text("Agent").font(Face.sans(Metrics.chrome, .semibold))
                Spacer()
                Text("需要你 1 · 工作中 1").font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkMuted)
            }
            VStack(alignment: .leading, spacing: 4) {
                Text("Codex · 修复登录测试").font(Face.sans(Metrics.chrome, .semibold))
                Text("● 工作中 · 运行 npm test").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.live)
            }
            .padding(6).background(Ink.accentSoft).clipShape(RoundedRectangle(cornerRadius: 6))
            VStack(alignment: .leading, spacing: 4) {
                Text("Claude 1").font(Face.sans(Metrics.chrome, .semibold))
                Text("终端 · 12 秒前有输出").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted)
            }
            .padding(6).background(Color.clear)
            VStack(alignment: .leading, spacing: 2) {
                Text("Codex · 补文档").font(Face.sans(Metrics.chrome))
                Text("✓ 已完成 · 3 分钟前").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted)
            }
            Spacer(minLength: 0)
        }
        .padding(Metrics.gutter)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(Ink.canvas)
    }
}

struct AgentStreamStatusFrozen: View {
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Text("Agent").font(Face.sans(Metrics.chrome, .semibold))
                Spacer()
                Text("需要你 1").font(Face.sans(Metrics.caption)).foregroundStyle(Change.changed)
            }
            VStack(alignment: .leading, spacing: 4) {
                Text("Codex · 修复登录测试").font(Face.sans(Metrics.chrome, .semibold))
                Text("等你确认 · rm -rf .cache").font(Face.mono(Metrics.caption)).foregroundStyle(Change.changed)
                HStack(spacing: 6) {
                    Text("允许").font(Face.sans(Metrics.caption)).padding(.horizontal, 8).padding(.vertical, 2)
                        .background(Ink.accentSoft).clipShape(RoundedRectangle(cornerRadius: 4))
                    Text("拒绝").font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkMuted)
                }
            }
            .padding(6).frame(maxWidth: .infinity, alignment: .leading)
            .background(Ink.accentSoft).clipShape(RoundedRectangle(cornerRadius: 6))
            VStack(alignment: .leading, spacing: 2) {
                Text("‹ Agent    Codex · 修复登录测试").font(Face.sans(Metrics.caption, .semibold))
                Text("● 工作中 · 运行 npm test · 0:42").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.inkMuted)
                Text("你\n修复登录页的单元测试").font(Face.mono(9)).foregroundStyle(Ink.inkFaint)
            }
            .padding(6).background(Ink.canvas).clipShape(RoundedRectangle(cornerRadius: 4))
            Spacer(minLength: 0)
        }
        .padding(Metrics.gutter)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(Ink.canvas)
    }
}

struct SessionStatusFrozen: View {
    var active: Bool
    var noProvider = false
    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Agent").font(Face.sans(Metrics.chrome, .semibold))
            if active {
                HStack(spacing: 6) {
                    Circle().fill(Ink.live).frame(width: 6, height: 6)
                    VStack(alignment: .leading, spacing: 1) {
                        Text("Codex · 修复登录测试").font(Face.sans(Metrics.chrome, .semibold))
                        Text("● 工作中 · 运行 npm test").font(Face.mono(Metrics.caption)).foregroundStyle(Ink.live)
                    }
                }
                .padding(6).frame(maxWidth: .infinity, alignment: .leading)
                .background(Ink.accentSoft).clipShape(RoundedRectangle(cornerRadius: 6))
            } else {
                VStack(alignment: .leading, spacing: 6) {
                    Text("还没有 Agent 会话。")
                        .font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkMuted)
                    if noProvider {
                        Text("先选择默认 Agent")
                            .font(Face.sans(Metrics.caption)).foregroundStyle(Change.changed)
                    } else {
                        Text("在下方写下任务，⌘↩ 交给 Codex。")
                            .font(Face.sans(Metrics.caption)).foregroundStyle(Ink.inkMuted)
                    }
                }
            }
            Spacer(minLength: 0)
        }
        .padding(Metrics.gutter)
        .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
        .background(Ink.canvas)
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
