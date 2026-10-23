import AppKit
import SwiftUI
import BerthAgent
import BerthTerminal

struct TerminalSurface: NSViewRepresentable {
    let session: TerminalSession
    var detached = false
    var focusIntent: TerminalFocusIntent?
    var dimTerminalCaret = false
    var bridgePromptKeys = false
    var onPromptKey: ((NSEvent) -> Void)?
    @ObservedObject private var settings = UISettings.shared
    @ObservedObject private var windows = TerminalWindows.shared
    func makeNSView(context: Context) -> TerminalHost {
        let host = TerminalHost()
        host.present(session, detached: detached)
        applyAppearance(session.terminal)
        host.bridgePromptKeys = bridgePromptKeys
        host.onPromptKey = onPromptKey
        host.focusIntent = resolvedFocusIntent(host: host)
        return host
    }
    func updateNSView(_ host: TerminalHost, context: Context) {
        host.present(session, detached: detached)
        applyAppearance(session.terminal)
        host.bridgePromptKeys = bridgePromptKeys
        host.onPromptKey = onPromptKey
        host.focusIntent = resolvedFocusIntent(host: host)
    }
    private func resolvedFocusIntent(host: TerminalHost) -> TerminalFocusIntent? {
        if let window = host.window, let responder = window.firstResponder as? NSView,
           let terminal = host.session?.terminal, responder !== terminal, !responder.isDescendant(of: host) {
            return nil
        }
        if focusIntent?.consumed == false { return focusIntent }
        if windows.returnFocus?.consumed == false { return windows.returnFocus }
        return nil
    }
    static func dismantleNSView(_ host: TerminalHost, coordinator: ()) { host.releasePresentation() }
    private func applyAppearance(_ view: HostedTerminalView) {
        let font = Face.nativeMono(12)
        if view.font.fontName != font.fontName || view.font.pointSize != font.pointSize { view.font = font }
        view.nativeBackgroundColor = NSColor(settings.terminalBackground)
        view.nativeForegroundColor = NSColor(settings.terminalForeground)
        view.caretColor = dimTerminalCaret ? .clear : NSColor.selectedControlColor
    }
}

struct TerminalPaneBody: View {
    @ObservedObject var session: TerminalSession
    var frozen: Bool
    var focusIntent: TerminalFocusIntent?
    var dimTerminalCaret = false
    var bridgePromptKeys = false
    var onPromptKey: ((NSEvent) -> Void)?
    var onSendCommandToAgent: ((CommandBlock) -> Void)?
    var agentRows: [AgentRow] = []
    var onEnterAgent: ((AgentRow) -> Void)?
    var focusedAgentCardID: UUID? = nil
    /// Warp block stream: PTY is hosted inside the active block card, never a sibling pane.
    var warpBlocks = true
    @State private var selectedBlockID: UUID?
    @ObservedObject private var windows = TerminalWindows.shared
    var body: some View {
        Group {
            if frozen {
                Text("$ pwd\n\(session.worktree)\n$ echo 'native session'\nnative session")
                    .font(Face.mono(11)).padding(10)
                    .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .topLeading)
            } else if windows.windows[session.id] != nil {
                VStack(spacing: 10) {
                    Image(systemName: "macwindow")
                    Text("此会话在独立窗口中 / Detached window")
                    Button("显示窗口") { windows.detach(session) }
                    Button("返回工作区") { windows.bringBack(session) }
                }.font(Face.sans(11)).frame(maxWidth: .infinity, maxHeight: .infinity)
            } else if warpBlocks, session.kind == "terminal" {
                // One scroll: completed cards + active card that hosts the single PTY.
                GeometryReader { geo in
                    SessionBlockStream(
                        session: session,
                        selectedID: selectedBlockID,
                        onSelect: { block in selectedBlockID = block.id },
                        onSendToAgent: onSendCommandToAgent,
                        agentRows: agentRows,
                        onEnterAgent: onEnterAgent,
                        focusedAgentCardID: focusedAgentCardID,
                        availableHeight: geo.size.height,
                        focusIntent: focusIntent,
                        dimTerminalCaret: dimTerminalCaret,
                        bridgePromptKeys: bridgePromptKeys,
                        onPromptKey: onPromptKey
                    )
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                // Native agent PTY (non-terminal kind): full canvas, not block-streamed.
                TerminalSurface(session: session, focusIntent: focusIntent, dimTerminalCaret: dimTerminalCaret,
                                bridgePromptKeys: bridgePromptKeys, onPromptKey: onPromptKey).id(session.id)
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }
}

func showTerminalFind(_ terminal: HostedTerminalView) {
    let item = NSMenuItem()
    item.tag = NSTextFinder.Action.showFindInterface.rawValue
    terminal.performTextFinderAction(item)
}
