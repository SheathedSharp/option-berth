import AppKit
import SwiftUI

/// Finder / Notes / System Settings–style sidebar backdrop.
///
/// Material `.sidebar` ships with macOS 10.14+ and is available on this
/// package's macOS 14 deployment target. Blending behind the window lets
/// the desktop wallpaper show through; state tracks active/inactive chrome.
struct SidebarMaterial: NSViewRepresentable {
    func makeNSView(context: Context) -> NSVisualEffectView {
        let view = NSVisualEffectView()
        view.material = .sidebar
        view.blendingMode = .behindWindow
        view.state = .followsWindowActiveState
        view.isEmphasized = true
        return view
    }

    func updateNSView(_ view: NSVisualEffectView, context: Context) {
        view.material = .sidebar
        view.blendingMode = .behindWindow
        view.state = .followsWindowActiveState
    }
}
