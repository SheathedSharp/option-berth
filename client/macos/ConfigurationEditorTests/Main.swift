import AppKit
import SwiftUI
import ScreenCaptureKit
@testable import BerthClient

@main @MainActor enum ConfigurationEditorChecks {
    static var checks = 0
    static func expect(_ condition: @autoclosure () -> Bool, _ message: String) throws {
        guard condition() else { throw NSError(domain: "ConfigurationEditorChecks", code: 1, userInfo: [NSLocalizedDescriptionKey: message]) }
        checks += 1
    }
    static func eventually(_ message: String, _ predicate: () -> Bool) async throws {
        let end = Date().addingTimeInterval(6)
        while !predicate(), Date() < end { try await Task.sleep(nanoseconds: 10_000_000) }
        try expect(predicate(), message)
    }
    static func find<T: NSView>(_ type: T.Type, in view: NSView) -> [T] {
        ((view as? T).map { [$0] } ?? []) + view.subviews.flatMap { find(type, in: $0) }
    }
    static func main() {
        let env = ProcessInfo.processInfo.environment
        guard env["BERTH_EDITOR_TEST"] == "1", let home = env["HOME"], env["CFFIXED_USER_HOME"] == home,
              let berth = env["BERTH_HOME"], berth.hasPrefix(home + "/") else {
            fputs("Explicit isolated HOME/BERTH_HOME required\n", stderr); exit(2)
        }
        let app = NSApplication.shared; app.setActivationPolicy(.accessory); app.finishLaunching()
        Task { @MainActor in
            do { try await run(root: URL(fileURLWithPath: berth)); print("ConfigurationEditorChecks: \(checks) native checks passed"); exit(0) }
            catch { fputs("FAIL: \(error.localizedDescription)\n", stderr); exit(1) }
        }
        app.run()
    }
    static func run(root: URL) async throws {
        let directory = root.appendingPathComponent("config-" + UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: directory) }
        let defaults = UserDefaults(suiteName: "oberth-editor-tests-" + UUID().uuidString)!
        let settings = UISettings(defaults: defaults, configurationDirectory: directory)
        let source = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 900, height: 640),
            styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: false)
        source.isReleasedWhenClosed = false
        let sourceHost = NSHostingView(rootView: SettingsEntryFixture(settings: settings))
        sourceHost.sizingOptions = []; source.contentView = sourceHost
        NSApp.activate(ignoringOtherApps: true); source.makeKeyAndOrderFront(nil)
        defer {
            if let sheet = source.attachedSheet { source.endSheet(sheet) }
            source.contentView = nil; source.close()
        }
        var entry: NSView?
        try await eventually("real settings sheet did not expose the internal editor entry geometry") {
            guard let content = source.attachedSheet?.contentView else { return false }
            entry = find(NSView.self, in: content).first { $0.identifier?.rawValue == "configuration.entry.theme" }
            return entry?.bounds.isEmpty == false
        }
        func pressEntry() throws {
            guard let entry, let target = entry.window else { throw CocoaError(.coderInvalidValue) }
            let point = entry.convert(NSPoint(x: entry.bounds.midX, y: entry.bounds.midY), to: nil)
            for type in [NSEvent.EventType.leftMouseDown, .leftMouseUp] {
                let event = NSEvent.mouseEvent(with: type, location: point, modifierFlags: [],
                    timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: target.windowNumber,
                    context: nil, eventNumber: 1, clickCount: 1, pressure: type == .leftMouseDown ? 1 : 0)!
                NSApp.postEvent(event, atStart: false)
            }
        }
        try pressEntry()
        var opened: ConfigurationEditorWindow?
        try await eventually("settings button did not open the native configuration window") {
            opened = NSApp.windows.compactMap { $0.delegate as? ConfigurationEditorWindow }.first { $0.model.document == .theme }
            return opened?.window?.isKeyWindow == true
        }
        let controller = opened!, model = controller.model
        guard let window = controller.window, let host = window.contentView else { fatalError("native editor window missing") }
        let expectedAppearance: NSAppearance.Name = settings.colorScheme == .dark ? .darkAqua : .aqua
        try expect(window.isOpaque && window.backgroundColor?.isEqual(NSColor(settings.canvasColor)) == true,
                   "configuration editor window did not use the configured canvas")
        try expect(window.appearance?.name == expectedAppearance,
                   "configuration editor window did not use the configured appearance")
        defer { window.contentView = nil; window.close() }
        source.attachedSheet?.makeKeyAndOrderFront(nil)
        try pressEntry()
        try await eventually("reopening an existing editor did not return its window") { window.isKeyWindow }
        try expect(NSApp.windows.compactMap { $0.delegate as? ConfigurationEditorWindow }.filter { $0.model.document == .theme }.count == 1,
                   "opening the same document duplicated its editor")
        try await eventually("editor did not load") { model.isLoaded && window.isKeyWindow }
        let url = directory.appendingPathComponent("theme.json")
        try expect(!FileManager.default.fileExists(atPath: url.path), "open wrote a configuration")
        try expect(!model.hasUnsavedChanges, "template is dirty before editing")
        guard let editor = find(ConfigurationTextView.self, in: host).first else { fatalError("native text view missing") }
        try expect(editor.backgroundColor?.isEqual(NSColor(settings.canvasColor)) == true && editor.textColor?.isEqual(NSColor(settings.inkColor)) == true,
                   "native editor text view did not use the configured canvas and ink")
        try await eventually("native editor did not become editable with the loaded text") { editor.isEditable && editor.string == model.text }
        window.makeFirstResponder(editor)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try await captureOwnedWindow(window, to: root.appendingPathComponent("configuration-template-composited-native.png"))
        let initial = editor.string
        let candidate = "{\n  // native editing fixture\n  \"schemaVersion\": 1,\n  \"colors\": {\"accent\": \"#123456\",},\n}\n"
        func replace(_ text: String) {
            editor.setSelectedRange(NSRange(location: 0, length: editor.string.utf16.count))
            editor.insertText(text, replacementRange: editor.selectedRange())
        }
        replace(candidate)
        try await eventually("valid native edit was not saved/applied") { settings.accentHex == "#123456" && !model.hasUnsavedChanges }
        let firstSave = try Data(contentsOf: url)
        try expect(firstSave == Data(candidate.utf8), "saved content lost comments")
        try expect(window.firstResponder === editor && editor.string == candidate, "theme update replaced editor/focus")
        editor.undoManager?.undo()
        try await eventually("native undo did not retain editing") { editor.string == initial && !model.hasUnsavedChanges }
        editor.undoManager?.redo()
        try await eventually("native redo was not applied") { editor.string == candidate && settings.accentHex == "#123456" && !model.hasUnsavedChanges }
        let saved = try Data(contentsOf: url)
        replace("{")
        try await eventually("invalid native edit has no diagnostic") { model.problem && !model.isSaving }
        var prompted = false
        let cancelledQuit = ConfigurationEditorWindows.allowTermination(models: [model]) { saving, names in
            prompted = !saving && names == ["theme.json"]
            return false
        }
        try expect(prompted && !cancelledQuit && model.isLoaded, "application quit discarded invalid configuration without consent")
        let invalidBytes = try Data(contentsOf: url)
        try expect(invalidBytes == saved && model.hasUnsavedChanges && editor.string == "{", "invalid edit changed disk or discarded draft")
        let external = Data("{\"schemaVersion\":1,\"colors\":{\"accent\":\"#ABCDEF\"}}\n".utf8)
        try external.write(to: url, options: .atomic)
        replace(candidate)
        try await eventually("external edit conflict was not surfaced") { model.problem && model.status.contains("外部") }
        let conflictBytes = try Data(contentsOf: url)
        try expect(conflictBytes == external && editor.string == candidate, "external edit or draft overwritten")
        model.reload() // Explicit reload, like confirming the UI's discard action.
        try await eventually("explicit reload did not show disk") { model.isLoaded && editor.string == String(decoding: external, as: UTF8.self) }
        window.makeFirstResponder(editor)
        editor.setMarkedText(candidate, selectedRange: NSRange(location: candidate.utf16.count, length: 0),
                             replacementRange: NSRange(location: 0, length: editor.string.utf16.count))
        try expect(editor.hasMarkedText(), "native composition fixture not marked")
        // Some OS versions do not notify the delegate for programmatic marking;
        // mirror the actual delegate path while retaining a real marked editor.
        model.edited(editor.string, composing: editor.hasMarkedText())
        try await Task.sleep(nanoseconds: 500_000_000)
        let duringIME = try Data(contentsOf: url)
        try expect(duringIME == external, "marked text was autosaved")
        editor.unmarkText()
        try await eventually("committed IME input was not saved") { settings.accentHex == "#123456" && !model.hasUnsavedChanges }
        let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: .command,
            timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber, context: nil,
            characters: "f", charactersIgnoringModifiers: "f", isARepeat: false, keyCode: 3)!
        try expect(editor.performKeyEquivalent(with: event), "native Find was not local to the editor")
        try await eventually("native find bar did not appear") { editor.enclosingScrollView?.isFindBarVisible == true }
        let hide = NSMenuItem(); hide.tag = NSTextFinder.Action.hideFindInterface.rawValue
        editor.performTextFinderAction(hide)
        try await Task.sleep(nanoseconds: 100_000_000)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try await captureOwnedWindow(window, to: root.appendingPathComponent("configuration-editor-composited-native.png"))
        let cleanQuit = ConfigurationEditorWindows.allowTermination(models: [model]) { _, _ in false }
        try expect(cleanQuit, "saved editor blocks application quit")
        try expect(controller.windowShouldClose(window), "clean editor cannot close")
        let draftModel = ConfigurationEditorModel(document: .settings, directory: directory, settings: settings)
        try await eventually("termination fixture did not load") { draftModel.isLoaded }
        draftModel.edited("{", composing: true)
        try expect(ConfigurationEditorWindows.allowTermination(models: [draftModel], ask: { _, _ in true }), "explicit draft discard cannot quit")
        try await Task.sleep(nanoseconds: 350_000_000)
        try expect(!FileManager.default.fileExists(atPath: directory.appendingPathComponent("settings.json").path), "discarding a draft queued a file write")
    }
    /// Capture only this harness's explicitly owned window. A view bitmap omits
    /// composited SwiftUI/AppKit layers and is not a visual-acceptance artifact.
    static func captureOwnedWindow(_ window: NSWindow, to url: URL) async throws {
        let content = try await SCShareableContent.excludingDesktopWindows(true, onScreenWindowsOnly: false)
        guard let owned = content.windows.first(where: {
            $0.windowID == CGWindowID(window.windowNumber) && $0.owningApplication?.processID == ProcessInfo.processInfo.processIdentifier
        }) else { throw NSError(domain: "EditorCapture", code: 1) }
        let configuration = SCStreamConfiguration()
        configuration.width = Int(window.frame.width * window.backingScaleFactor)
        configuration.height = Int(window.frame.height * window.backingScaleFactor)
        configuration.showsCursor = false
        let image = try await SCScreenshotManager.captureImage(contentFilter: SCContentFilter(desktopIndependentWindow: owned), configuration: configuration)
        guard let png = NSBitmapImageRep(cgImage: image).representation(using: .png, properties: [:]) else { throw NSError(domain: "EditorCapture", code: 2) }
        try png.write(to: url)
    }

}

private struct SettingsEntryFixture: View {
    let settings: UISettings
    @State private var presented = true
    var body: some View {
        Color.clear.sheet(isPresented: $presented) {
            SettingsSheet(settings: settings, onClose: { presented = false })
                .overlayPreferenceValue(ConfigurationEntryAnchors.self) { anchors in
                    GeometryReader { geometry in
                        if let anchor = anchors["theme"] {
                            let rect = geometry[anchor]
                            ConfigurationEntryMarker().frame(width: rect.width, height: rect.height)
                                .position(x: rect.midX, y: rect.midY)
                        }
                    }.allowsHitTesting(false)
                }
        }
    }
}

private struct ConfigurationEntryMarker: NSViewRepresentable {
    func makeNSView(context: Context) -> NSView {
        let view = NSView(); view.identifier = NSUserInterfaceItemIdentifier("configuration.entry.theme"); return view
    }
    func updateNSView(_ view: NSView, context: Context) {}
}
