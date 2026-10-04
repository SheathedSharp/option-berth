import AppKit
import SwiftUI
import Darwin

// Compile the real sheet, request state, CLI, and brand/UI settings. The proposal
// adapter supplies only the two fields this view reads; no service model/daemon.
struct GroupInitResult { let path: String; let yaml: String }

@main
enum DraftSheetInteractionTests {
    static let original = "name: original\nservices: []\n"
    static let candidate = "name: candidate\nservices: []\n"
    static func require(_ condition: @autoclosure () -> Bool, _ message: String) {
        if !condition() { fatalError(message) }
    }
    @MainActor static func pump(_ seconds: Double = 0.05) {
        let end = Date().addingTimeInterval(seconds)
        while Date() < end { RunLoop.main.run(until: Date().addingTimeInterval(0.005)) }
    }
    @MainActor static func eventually(_ message: String, _ condition: () -> Bool) {
        let end = Date().addingTimeInterval(3)
        while !condition(), Date() < end { pump(0.01) }
        require(condition(), message)
    }
    @MainActor static func main() {
        if CommandLine.arguments.dropFirst().first == "init" { fixture(); return }
        // Supply an isolated HOME/CFFIXED_USER_HOME/BERTH_HOME when running this
        // AppKit check, before UserDefaults or font settings are initialized.
        let app = NSApplication.shared
        app.setActivationPolicy(.accessory); app.finishLaunching()
        let binary = URL(fileURLWithPath: CommandLine.arguments[0]).standardizedFileURL.path
        setenv("BERTH_BIN", binary, 1)
        for mode in ["success", "failure", "cancel", "disappear"] { checkSheet(mode) }
    }
    @MainActor static func checkSheet(_ mode: String) {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("draft-window-" + UUID().uuidString)
        try! FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: root) }
        try! mode.write(to: root.appendingPathComponent("mode"), atomically: true, encoding: .utf8)
        var written: String?
        var cancelled = false
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 600, height: 440),
                              styleMask: [.titled], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        let view = NSHostingView(rootView: AddProjectSheet(
            result: GroupInitResult(path: root.appendingPathComponent("oberth.yaml").path, yaml: original),
            onWrite: { written = $0 }, onCancel: { cancelled = true }))
        window.contentView = view; window.makeKeyAndOrderFront(nil)
        defer { window.contentView = nil; window.close() }
        pump(0.2)
        guard let editor = textView(view) else { fatalError("native TextEditor missing") }
        require(editor.string == original, "initial editor bytes changed")
        click(window, at: NSPoint(x: 350, y: 23)) // Agent button in fixed 600x440 sheet.
        let pidFile = root.appendingPathComponent("pid")
        eventually("native agent button did not start the isolated CLI") {
            FileManager.default.fileExists(atPath: pidFile.path)
        }
        let pid = pid_t(try! String(contentsOf: pidFile, encoding: .utf8))!
        pump(0.15) // Deliver the fixture's `done` frame on the real main run loop.
        require(editor.string == original, "done frame prematurely replaced the editor")
        click(window, at: NSPoint(x: 530, y: 23))
        require(written == nil, "write was enabled before successful command exit")
        if mode == "cancel" {
            click(window, at: NSPoint(x: 440, y: 23))
            require(cancelled, "native Cancel did not invoke callback")
        } else if mode == "disappear" {
            window.contentView = nil
        } else {
            try! Data().write(to: root.appendingPathComponent("release"))
        }
        eventually("sheet lifecycle did not reap its real child") { reaped(pid) }
        pump(0.1)
        if mode == "success" {
            require(editor.string == candidate, "successful exit did not publish candidate")
            click(window, at: NSPoint(x: 530, y: 23))
            require(written == candidate, "native Write did not deliver exact candidate bytes")
        } else {
            require(editor.string == original, "failed/cancelled/disappeared request changed editor")
            require(written == nil, "failed/cancelled/disappeared request wrote content")
        }
        print("PASS: native draft \(mode), real button/window lifecycle and child reap")
    }
    @MainActor static func click(_ window: NSWindow, at point: NSPoint) {
        // Dispatch only to this fixture window. No global mouse events, screen
        // scraping, user clipboard, accessibility permission or private APIs.
        for kind in [NSEvent.EventType.leftMouseDown, .leftMouseUp] {
            window.sendEvent(NSEvent.mouseEvent(with: kind, location: point, modifierFlags: [],
                timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber,
                context: nil, eventNumber: 1, clickCount: 1, pressure: 1)!)
        }
        pump()
    }
    @MainActor static func textView(_ root: NSView) -> NSTextView? {
        if let editor = root as? NSTextView { return editor }
        for child in root.subviews { if let editor = textView(child) { return editor } }
        return nil
    }
    static func reaped(_ pid: pid_t) -> Bool {
        var info = siginfo_t()
        return waitid(P_PID, id_t(pid), &info, WEXITED | WNOHANG | WNOWAIT) == -1 && errno == ECHILD
    }
    static func fixture() {
        signal(SIGTERM, SIG_IGN); signal(SIGALRM, SIG_DFL); alarm(6)
        let root = URL(fileURLWithPath: FileManager.default.currentDirectoryPath)
        let mode = try! String(contentsOf: root.appendingPathComponent("mode"), encoding: .utf8)
        let event = try! JSONSerialization.data(withJSONObject: ["type": "done", "yaml": candidate])
        FileHandle.standardOutput.write(event + Data([10]))
        try! String(getpid()).write(to: root.appendingPathComponent("pid"), atomically: true, encoding: .utf8)
        while !FileManager.default.fileExists(atPath: root.appendingPathComponent("release").path) { usleep(10_000) }
        exit(mode == "failure" ? 7 : 0)
    }
}
