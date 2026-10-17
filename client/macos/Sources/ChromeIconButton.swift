import SwiftUI

/// Icon-only chrome control with help text — log pane / toolbar density.
///
/// HIG toolbars prefer symbols without borders; help carries the name.
/// Skills: operate (familiar affordance), quieter (no text chrome clutter).
struct ChromeIconButton: View {
    let systemImage: String
    let help: String
    var tone: Color = Ink.inkMuted
    var emphasized: Bool = false
    let action: () -> Void

    @State private var hovering = false

    var body: some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .font(.system(size: 12, weight: emphasized ? .semibold : .regular))
                .foregroundStyle(emphasized ? Ink.accent : (hovering ? Ink.ink : tone))
                .frame(width: Metrics.hit, height: Metrics.hit)
                .background(
                    RoundedRectangle(cornerRadius: 5, style: .continuous)
                        .fill(hovering ? Ink.sunken : Color.clear)
                )
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .help(help)
        .accessibilityLabel(help)
        .onHover { now in
            withAnimation(Motion.hover) { hovering = now }
        }
    }
}

/// Binds the NSWindow title to the current project display name (HIG: never
/// the app name as the useful title). Freeze / ImageRenderer paths have no window.
struct WindowTitleBinder: NSViewRepresentable {
    let title: String

    func makeNSView(context: Context) -> NSView {
        let view = NSView(frame: .zero)
        view.setAccessibilityElement(false)
        return view
    }

    func updateNSView(_ nsView: NSView, context: Context) {
        guard let window = nsView.window ?? NSApp.windows.first(where: {
            $0.identifier?.rawValue == "workspace" || $0.title.hasPrefix("option") || $0.isKeyWindow
        }) else { return }
        if window.sheetParent != nil { return }
        if window.title != title {
            window.title = title
        }
    }
}
