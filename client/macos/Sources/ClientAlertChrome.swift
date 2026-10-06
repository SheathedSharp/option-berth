import AppKit

/// Keeps native alerts in the same visual system as the client surface.
///
/// NSAlert is still the right primitive for keyboard focus, VoiceOver, and
/// destructive actions, but its window otherwise inherits the system material
/// (and can flash white while a custom dark theme is active). Configure the
/// already-created alert window without replacing the native alert layout or
/// button semantics.
enum ClientAlert {
    static func make(settings: UISettings = .shared) -> NSAlert {
        let alert = NSAlert()
        apply(to: alert, settings: settings)
        return alert
    }

    static func apply(to alert: NSAlert, settings: UISettings = .shared) {
        let window = alert.window
        window.isOpaque = true
        window.backgroundColor = NSColor(settings.canvasColor)
        window.appearance = NSAppearance(named: settings.colorScheme == .dark ? .darkAqua : .aqua)
    }
}
