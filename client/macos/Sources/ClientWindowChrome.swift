import AppKit
import SwiftUI

/// Binds a SwiftUI presentation's native window to the client's paper surface.
///
/// SwiftUI paints the content background, but the surrounding NSWindow can keep
/// the macOS material/white chrome visible while a sheet is being resized. The
/// bridge is deliberately passive: it never owns presentation, focus, or size;
/// it only applies the already selected canvas color after AppKit attaches the
/// view to its window.
///
/// When `vibrancyBehindWindow` is true (main workspace only), the window stays
/// non-opaque so a sidebar `NSVisualEffectView` can blend behind the window.
/// Sheets keep the opaque paper path.
struct ClientWindowChrome: NSViewRepresentable {
    let color: NSColor
    let dark: Bool
    var vibrancyBehindWindow = false

    init(color: NSColor, dark: Bool, vibrancyBehindWindow: Bool = false) {
        self.color = color; self.dark = dark; self.vibrancyBehindWindow = vibrancyBehindWindow
    }

    func makeNSView(context: Context) -> WindowObserver {
        WindowObserver(color: color, dark: dark, vibrancyBehindWindow: vibrancyBehindWindow)
    }

    func updateNSView(_ nsView: WindowObserver, context: Context) {
        nsView.color = color
        nsView.dark = dark
        nsView.vibrancyBehindWindow = vibrancyBehindWindow
        nsView.apply()
    }

    final class WindowObserver: NSView {
        var color: NSColor
        var dark: Bool
        var vibrancyBehindWindow: Bool

        init(color: NSColor, dark: Bool, vibrancyBehindWindow: Bool) {
            self.color = color; self.dark = dark; self.vibrancyBehindWindow = vibrancyBehindWindow
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
            if vibrancyBehindWindow {
                window.isOpaque = false
                window.backgroundColor = .clear
            } else {
                window.isOpaque = true
                window.backgroundColor = color
            }
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
    var vibrancyBehindWindow = false

    func body(content: Content) -> some View {
        content
            .background {
                // A tiny non-interactive view is enough to observe the native
                // presentation window without changing SwiftUI layout.
                ClientWindowChrome(color: NSColor(settings.canvasColor),
                                   dark: settings.colorScheme == .dark,
                                   vibrancyBehindWindow: vibrancyBehindWindow)
                    .frame(width: 1, height: 1)
                    .allowsHitTesting(false)
            }
            .background(vibrancyBehindWindow ? Color.clear : Ink.canvas)
            .preferredColorScheme(settings.colorScheme)
    }
}

extension View {
    /// Apply the existing paper surface to the surrounding native window.
    /// Pass `vibrancyBehindWindow: true` on the main workspace so sidebar material
    /// can blend behind the window; sheets keep the default opaque path.
    func clientWindowChrome(vibrancyBehindWindow: Bool = false) -> some View {
        modifier(ClientWindowChromeModifier(vibrancyBehindWindow: vibrancyBehindWindow))
    }
}
