import AppKit
import SwiftUI

/// Binds a SwiftUI presentation's native window to the client's paper surface.
///
/// SwiftUI paints the content background, but the surrounding NSWindow can keep
/// the macOS material/white chrome visible while a sheet is being resized. The
/// bridge is deliberately passive: it never owns presentation, focus, or size;
/// it only applies the already selected canvas color after AppKit attaches the
/// view to its window.
struct ClientWindowChrome: NSViewRepresentable {
    let color: NSColor
    let dark: Bool

    init(color: NSColor, dark: Bool) { self.color = color; self.dark = dark }

    func makeNSView(context: Context) -> WindowObserver {
        WindowObserver(color: color)
    }

    func updateNSView(_ nsView: WindowObserver, context: Context) {
        nsView.color = color
        nsView.dark = dark
        nsView.apply()
    }

    final class WindowObserver: NSView {
        var color: NSColor
        var dark: Bool

        init(color: NSColor, dark: Bool) {
            self.color = color; self.dark = dark
            super.init(frame: .zero)
            setAccessibilityElement(false)
        }

        required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

        override func viewDidMoveToWindow() {
            super.viewDidMoveToWindow()
            apply()
        }

        func apply() {
            guard let window else { return }
            window.isOpaque = true
            window.backgroundColor = color
            window.appearance = NSAppearance(named: dark ? .darkAqua : .aqua)
            // Sheets otherwise retain a translucent titlebar material above a
            // solid paper surface. Keep native buttons and dismissal intact.
            if window.sheetParent != nil {
                window.titlebarAppearsTransparent = true
                window.titleVisibility = .hidden
            }
        }
    }
}

private struct ClientWindowChromeModifier: ViewModifier {
    @ObservedObject private var settings = UISettings.shared

    func body(content: Content) -> some View {
        content
            .background {
                // A tiny non-interactive view is enough to observe the native
                // presentation window without changing SwiftUI layout.
                ClientWindowChrome(color: NSColor(settings.canvasColor), dark: settings.colorScheme == .dark)
                    .frame(width: 1, height: 1)
                    .allowsHitTesting(false)
            }
            .background(Ink.canvas)
            .preferredColorScheme(settings.colorScheme)
    }
}

extension View {
    /// Apply the existing paper surface to the surrounding native window.
    func clientWindowChrome() -> some View {
        modifier(ClientWindowChromeModifier())
    }
}
