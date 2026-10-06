import AppKit
import SwiftUI

/// One in-flight coordinated write plus one replaceable debounce. Rendering or
/// typing does not enqueue an unbounded queue of filesystem operations.
@MainActor final class ConfigurationEditorModel: ObservableObject {
    @Published private(set) var text = ""
    @Published private(set) var status = "正在载入…"
    @Published private(set) var problem = false
    @Published private(set) var isLoaded = false
    @Published private(set) var isSaving = false
    let document: ConfigurationDocument
    private let file: ConfigurationEditorFile
    private let settings: UISettings
    private let queue = DispatchQueue(label: "option-berth.configuration-editor", qos: .userInitiated)
    private var scheduled: DispatchWorkItem?
    private var savedText = ""
    private var revision = 0
    private var composing = false
    private var closed = false
    var hasUnsavedChanges: Bool { text != savedText || isSaving }
    init(document: ConfigurationDocument, directory: URL, settings: UISettings) {
        self.document = document; self.settings = settings
        file = ConfigurationEditorFile(document: document, directory: directory)
        reload()
    }
    deinit { scheduled?.cancel() }
    func reload() {
        guard !isSaving, !closed else { return }
        scheduled?.cancel(); revision += 1
        let version = revision
        isLoaded = false; problem = false; status = "正在载入…"
        queue.async { [weak self, file] in
            let result = Result { try file.load() }
            DispatchQueue.main.async { [weak self] in
                guard let self, !self.closed, self.revision == version else { return }
                switch result {
                case .success(let source):
                    self.text = source; self.savedText = source; self.isLoaded = true
                    self.status = "有效修改自动保存；打开文件不会改写内容。"
                case .failure: self.problem = true; self.status = "无法载入配置；原文件未修改。"
                }
            }
        }
    }
    func edited(_ value: String, composing: Bool) {
        guard isLoaded, !closed else { return }
        text = value; self.composing = composing; revision += 1
        scheduled?.cancel(); scheduled = nil
        status = composing ? "正在输入…" : "正在编辑…"; problem = false
        if !composing { scheduleSave() }
    }
    private func scheduleSave() {
        guard !closed, !composing else { return }
        let work = DispatchWorkItem { [weak self] in self?.saveLatest() }
        scheduled?.cancel(); scheduled = work
        DispatchQueue.main.asyncAfter(deadline: .now() + 0.3, execute: work)
    }
    private func saveLatest() {
        scheduled = nil
        guard !closed, isLoaded, !composing, !isSaving else { return }
        let source = text, version = revision
        isSaving = true; status = "正在校验并保存…"
        queue.async { [weak self, file] in
            let result = Result { try file.save(source) }
            DispatchQueue.main.async { [weak self] in
                guard let self, !self.closed else { return }
                self.isSaving = false
                if case .success = result {
                    self.savedText = source
                    self.settings.reloadConfiguration()
                }
                guard self.revision == version else { self.scheduleSave(); return }
                switch result {
                case .success: self.status = "已保存 · 即时生效"; self.problem = false
                case .failure(let error):
                    self.problem = true
                    if case ConfigurationEditingError.conflict = error { self.status = error.localizedDescription }
                    else if let error = error as? ClientConfigurationError { self.status = error.localizedDescription }
                    else { self.status = "JSON 或文件无效；草稿已保留，磁盘和最近有效配置未改变。" }
                }
            }
        }
    }
    func abandon() {
        // The window will not close while a coordinated save is in flight.
        guard !isSaving else { return }
        closed = true; scheduled?.cancel(); scheduled = nil
    }
}

@MainActor final class ConfigurationEditorWindows {
    static let shared = ConfigurationEditorWindows()
    private var windows: [String: ConfigurationEditorWindow] = [:]
    /// Application termination does not send windowShouldClose to utility
    /// panels. Do not silently lose invalid drafts or exit during a disk write.
    func confirmTermination() -> Bool {
        let models = windows.values.map(\.model)
        return Self.allowTermination(models: models) { saving, names in
            let alert = NSAlert()
            alert.messageText = saving ? "配置正在保存" : "仍有未保存的配置草稿"
            alert.informativeText = saving ? "请等待保存结束后再退出。" : names.joined(separator: "、") + " 的当前编辑尚未保存。"
            alert.addButton(withTitle: "返回编辑")
            if !saving { alert.addButton(withTitle: "丢弃草稿并退出") }
            let discard = alert.runModal() == .alertSecondButtonReturn
            if !discard, let editor = windows.values.first(where: { $0.model.hasUnsavedChanges }) {
                editor.showWindow(nil); editor.window?.makeKeyAndOrderFront(nil)
            }
            return discard
        }
    }
    static func allowTermination(models: [ConfigurationEditorModel], ask: (Bool, [String]) -> Bool) -> Bool {
        let pending = models.filter { $0.hasUnsavedChanges }
        if pending.isEmpty { return true }
        let saving = pending.contains { $0.isSaving }
        let discard = ask(saving, pending.map { $0.document.filename }.sorted())
        // An NSAlert runs a nested event loop: autosave may have begun while
        // the confirmation was visible. Re-read ownership after it returns.
        guard !saving, !models.contains(where: { $0.isSaving }), discard else { return false }
        models.forEach { $0.abandon() }
        return true
    }
    func open(_ document: ConfigurationDocument, settings: UISettings) {
        guard let directory = settings.configurationDirectory else { return }
        let key = directory.appendingPathComponent(document.filename).standardizedFileURL.path
        if let controller = windows[key] { controller.showWindow(nil); controller.window?.makeKeyAndOrderFront(nil); return }
        let model = ConfigurationEditorModel(document: document, directory: directory, settings: settings)
        let controller = ConfigurationEditorWindow(model: model, settings: settings)
        controller.didClose = { [weak self] in self?.windows.removeValue(forKey: key) }
        windows[key] = controller
        controller.showWindow(nil); controller.window?.makeKeyAndOrderFront(nil)
    }
}

@MainActor final class ConfigurationEditorWindow: NSWindowController, NSWindowDelegate {
    let model: ConfigurationEditorModel
    private let settings: UISettings
    var didClose: (() -> Void)?
    init(model: ConfigurationEditorModel, settings: UISettings) {
        self.model = model; self.settings = settings
        // A utility document must not dispatch project-navigation commands.
        let window = NSPanel(contentRect: NSRect(x: 0, y: 0, width: 760, height: 550),
            styleMask: [.titled, .closable, .resizable], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false; window.isFloatingPanel = false
        window.becomesKeyOnlyIfNeeded = false; window.hidesOnDeactivate = false
        window.minSize = NSSize(width: 520, height: 340)
        window.title = model.document.title + " · " + model.document.filename
        window.isOpaque = true
        window.backgroundColor = NSColor(settings.canvasColor)
        window.appearance = NSAppearance(named: settings.colorScheme == .dark ? .darkAqua : .aqua)
        super.init(window: window)
        window.delegate = self
        window.contentView = NSHostingView(rootView: ConfigurationEditorView(model: model, settings: settings, reload: { [weak self] in self?.reload() }))
        window.center()
    }
    required init?(coder: NSCoder) { fatalError("init(coder:) is not supported") }
    private func mayDiscard() -> Bool {
        guard !model.isSaving else { NSSound.beep(); return false }
        guard model.hasUnsavedChanges else { return true }
        let alert = NSAlert()
        alert.messageText = "保留未保存的编辑？"
        alert.informativeText = "最近有效修改已自动保存。当前草稿尚未保存，丢弃后无法恢复。"
        alert.addButton(withTitle: "继续编辑"); alert.addButton(withTitle: "丢弃未保存草稿")
        let discard = alert.runModal() == .alertSecondButtonReturn
        return discard && !model.isSaving
    }
    private func reload() { if mayDiscard() { model.reload() } }
    func windowShouldClose(_ sender: NSWindow) -> Bool {
        guard mayDiscard() else { return false }
        model.abandon(); return true
    }
    func windowWillClose(_ notification: Notification) { model.abandon(); didClose?() }
}

struct ConfigurationEditorView: View {
    @ObservedObject var model: ConfigurationEditorModel
    @ObservedObject var settings: UISettings
    let reload: () -> Void
    var body: some View {
        VStack(spacing: 0) {
            HStack {
                VStack(alignment: .leading, spacing: 3) {
                    Text(model.document.title).font(.system(size: 16, weight: .semibold))
                    Text("支持注释与尾逗号 · 有效修改自动保存").font(.system(size: 11)).foregroundStyle(.secondary)
                }
                Spacer()
                Button("复制草稿") {
                    NSPasteboard.general.clearContents(); NSPasteboard.general.setString(model.text, forType: .string)
                }.disabled(!model.isLoaded)
                Button("重新载入", action: reload).disabled(model.isSaving)
            }.padding(16)
            Divider()
            ConfigurationEditorText(model: model, settings: settings)
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            Divider()
            HStack(alignment: .top, spacing: 8) {
                Image(systemName: model.problem ? "exclamationmark.circle" : "checkmark.circle")
                Text(model.status).font(.system(size: 11)).textSelection(.enabled)
                Spacer(minLength: 0)
                Text(model.document.filename).font(.system(size: 10, design: .monospaced)).foregroundStyle(.secondary)
            }.foregroundStyle(model.problem ? Color.orange : Color.secondary).padding(12)
        }.frame(minWidth: 500, minHeight: 300)
            .background(settings.canvasColor)
            .foregroundStyle(settings.inkColor)
            .preferredColorScheme(settings.colorScheme)
    }
}

/// Native editing remains intact. Only Find is handled locally, because this
/// utility window intentionally opts out of workspace command routing.
final class ConfigurationTextView: NSTextView {
    var settled: ((String, Bool) -> Void)?
    override func unmarkText() {
        super.unmarkText()
        settled?(string, hasMarkedText())
    }
    override func performKeyEquivalent(with event: NSEvent) -> Bool {
        let keys = event.modifierFlags.intersection([.command, .option, .control, .shift])
        if window?.firstResponder === self, !hasMarkedText(), keys == .command, event.charactersIgnoringModifiers == "f" {
            let item = NSMenuItem(); item.tag = NSTextFinder.Action.showFindInterface.rawValue
            performTextFinderAction(item); return true
        }
        return super.performKeyEquivalent(with: event)
    }
}

private struct ConfigurationEditorText: NSViewRepresentable {
    @ObservedObject var model: ConfigurationEditorModel
    @ObservedObject var settings: UISettings
    func makeCoordinator() -> Coordinator { Coordinator(model) }
    func makeNSView(context: Context) -> NSScrollView {
        let scroll = NSScrollView()
        scroll.hasVerticalScroller = true; scroll.autohidesScrollers = true
        scroll.drawsBackground = true; scroll.backgroundColor = NSColor(settings.canvasColor)
        let editor = ConfigurationTextView(frame: scroll.bounds)
        editor.isRichText = false; editor.allowsUndo = true; editor.usesFindBar = true
        editor.isAutomaticQuoteSubstitutionEnabled = false; editor.isAutomaticDashSubstitutionEnabled = false
        editor.isAutomaticTextReplacementEnabled = false; editor.isAutomaticSpellingCorrectionEnabled = false
        editor.isContinuousSpellCheckingEnabled = false
        editor.font = NSFont.monospacedSystemFont(ofSize: 12, weight: .regular)
        editor.drawsBackground = true
        editor.backgroundColor = NSColor(settings.canvasColor)
        editor.textColor = NSColor(settings.inkColor)
        editor.insertionPointColor = NSColor(settings.inkColor)
        editor.textContainerInset = NSSize(width: 14, height: 12)
        editor.isVerticallyResizable = true; editor.isHorizontallyResizable = false
        editor.autoresizingMask = [.width]; editor.textContainer?.widthTracksTextView = true
        editor.setAccessibilityIdentifier("configuration.editor")
        editor.setAccessibilityLabel(model.document.filename + " 配置编辑器")
        editor.delegate = context.coordinator
        editor.settled = { [weak model] text, composing in model?.edited(text, composing: composing) }
        scroll.documentView = editor
        return scroll
    }
    func updateNSView(_ scroll: NSScrollView, context: Context) {
        guard let editor = scroll.documentView as? ConfigurationTextView else { return }
        context.coordinator.model = model
        scroll.backgroundColor = NSColor(settings.canvasColor)
        editor.backgroundColor = NSColor(settings.canvasColor)
        editor.textColor = NSColor(settings.inkColor)
        editor.insertionPointColor = NSColor(settings.inkColor)
        editor.isEditable = model.isLoaded
        if editor.string != model.text, !editor.hasMarkedText() {
            editor.string = model.text
            editor.undoManager?.removeAllActions() // Explicit reload, never a theme repaint.
        }
    }
    final class Coordinator: NSObject, NSTextViewDelegate {
        var model: ConfigurationEditorModel
        init(_ model: ConfigurationEditorModel) { self.model = model }
        func textDidChange(_ notification: Notification) {
            guard let editor = notification.object as? NSTextView else { return }
            model.edited(editor.string, composing: editor.hasMarkedText())
        }
    }
}
