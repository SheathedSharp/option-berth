import SwiftUI

/// Compact workspace controls for hosted tests and freeze-path chrome.
/// Live windows mount the same actions via `BoardView` `.toolbar` items instead
/// of embedding this strip in the content canvas.
struct WorkspaceToolbar: View {
    var frozen = false
    var sessionCount: Int
    var recoveryPending: Bool
    let openActions: () -> Void
    let openSessions: () -> Void
    let openRecovery: () -> Void
    let openUpdates: () -> Void
    let openSettings: () -> Void
    var openGuide: () -> Void = {}

    var body: some View {
        HStack(spacing: Metrics.unit) {
            Wordmark(size: Metrics.chrome)
            Spacer(minLength: Metrics.unit)
            chromeButton(symbol: "magnifyingglass", title: "命令", shortcut: "⇧⌘P",
                         id: "commands", action: openActions)
                .tourAnchor(.commands)
                .help("搜索工作区动作 / Command panel")
            chromeButton(symbol: "rectangle.on.rectangle",
                         title: "会话 \(sessionCount)", id: "sessions", action: openSessions)
                .tourAnchor(.sessions)
            chromeButton(symbol: "clock.arrow.circlepath",
                         title: recoveryPending ? "恢复 · 待确认" : "恢复",
                         id: "recovery", action: openRecovery)
                .tourAnchor(.recovery)
            if frozen {
                Image(systemName: "ellipsis.circle")
                    .font(.system(size: 12, weight: .regular))
                    .frame(minWidth: Metrics.hit, minHeight: Metrics.hit)
                    .tourAnchor(.more)
            } else {
                Menu {
                    Button("检查更新… / Check for updates…", action: openUpdates)
                    Button("设置… / Settings…", action: openSettings)
                    Button("使用指引… / Getting started…", action: openGuide)
                } label: {
                    Image(systemName: "ellipsis.circle")
                        .font(.system(size: 12, weight: .regular))
                        .frame(minWidth: Metrics.hit, minHeight: Metrics.hit)
                        .contentShape(Rectangle())
                }
                .menuStyle(.borderlessButton)
                .fixedSize()
                .accessibilityLabel("更多工作区操作")
                .accessibilityIdentifier("workspace.toolbar.more")
                .tourAnchor(.more)
            }
        }
        .controlSize(.small)
        .buttonStyle(.plain)
        .foregroundStyle(Ink.inkMuted)
        .padding(.horizontal, Metrics.gutter)
        .frame(height: Metrics.chromeHeight)
        .background(Ink.canvas)
    }

    private func chromeButton(symbol: String, title: String, shortcut: String? = nil,
                              id: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            HStack(spacing: Metrics.tabPadV + 1) {
                Image(systemName: symbol)
                    .font(.system(size: 11, weight: .medium))
                Text(title)
                    .font(Face.sans(Metrics.chrome))
                    .lineLimit(1)
                if let shortcut {
                    Text(shortcut)
                        .font(Face.mono(Metrics.caption))
                        .foregroundStyle(Ink.inkFaint)
                }
            }
            .padding(.horizontal, Metrics.controlPadH)
            .padding(.vertical, Metrics.controlPadV)
            .frame(minHeight: Metrics.hit)
            .contentShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
        }
        .accessibilityIdentifier("workspace.toolbar." + id)
    }
}

/// Project face switcher — system segmented control live; freeze-safe
/// segment strip under ImageRenderer (NSSegmentedControl does not paint offscreen).
struct ProjectFacePicker: View {
    let projectName: String
    @Binding var scope: Scope
    var gitChanged: Int = 0
    var frozen: Bool = false
    var servicesHelp: String = ""
    var codeHelp: String = ""
    var terminalHelp: String = ""

    private enum ProjectFace: String, CaseIterable, Identifiable {
        case services, code, terminal
        var id: String { rawValue }
        var title: String {
            switch self {
            case .services: return "服务"
            case .code: return "Git"
            case .terminal: return "会话"
            }
        }
    }

    private var current: ProjectFace {
        switch scope {
        case .code: return .code
        case .terminal, .console: return .terminal
        case .services: return .services
        }
    }

    private var selection: Binding<ProjectFace> {
        Binding(
            get: { current },
            set: { face in
                switch face {
                case .services: scope = .services(projectName)
                case .code: scope = .code(projectName)
                case .terminal: scope = .terminal(projectName)
                }
            }
        )
    }

    var body: some View {
        Group {
            if frozen {
                freezeSegments
            } else {
                Picker("项目面", selection: selection) {
                    ForEach(ProjectFace.allCases) { face in
                        Text(label(for: face)).tag(face)
                    }
                }
                .pickerStyle(.segmented)
                .labelsHidden()
                .controlSize(.regular)
            }
        }
        .frame(maxWidth: 280)
        .help(helpText)
        .accessibilityIdentifier("workspace.projectFace")
        .tourAnchor(.facts)
    }

    private var freezeSegments: some View {
        HStack(spacing: 0) {
            ForEach(ProjectFace.allCases) { face in
                let selected = current == face
                Text(label(for: face))
                    .font(Face.sans(Metrics.chrome, selected ? .semibold : .regular))
                    .foregroundStyle(selected ? Ink.ink : Ink.inkMuted)
                    .frame(maxWidth: .infinity)
                    .padding(.vertical, Metrics.controlPadV + 1)
                    .background(selected ? Ink.canvas : Color.clear)
                    .contentShape(Rectangle())
            }
        }
        .padding(2)
        .background(Ink.sunken)
        .clipShape(RoundedRectangle(cornerRadius: 7, style: .continuous))
        .overlay(
            RoundedRectangle(cornerRadius: 7, style: .continuous)
                .strokeBorder(Ink.line, lineWidth: 1)
        )
    }

    private func label(for face: ProjectFace) -> String {
        if face == .code, gitChanged > 0 { return "Git · \(gitChanged)" }
        return face.title
    }

    private var helpText: String {
        switch current {
        case .services: return servicesHelp
        case .code: return codeHelp
        case .terminal: return terminalHelp
        }
    }
}

/// Legacy underline tab retained for any external host that still builds it.
/// Prefer `ProjectFacePicker` for new chrome.
struct WorkspaceTab: View {
    let title: String
    let symbol: String
    let selected: Bool
    let action: () -> Void

    var body: some View {
        Button(action: action) {
            Label(title, systemImage: symbol)
                .font(Face.sans(Metrics.chrome, selected ? .semibold : .regular))
                .foregroundStyle(selected ? Ink.accent : Ink.inkMuted)
                .padding(.horizontal, Metrics.tabPadH)
                .padding(.vertical, Metrics.tabPadV)
                .frame(minHeight: Metrics.hit)
                .background(selected ? Ink.accentSoft : Color.clear)
                .clipShape(RoundedRectangle(cornerRadius: 6, style: .continuous))
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}
