import AppKit
import SwiftUI
import BerthTerminal
import Darwin
@testable import BerthClient

extension ClientChecks {
    // A native button can track synchronously inside mouseDown on older macOS.
    // Queue the release first so its tracking loop can consume it. Newer
    // nontracking implementations leave it for the explicit dispatch below.
    static func guideClick(_ window: NSWindow, x: CGFloat, y: CGFloat = 30) {
        func event(_ kind: NSEvent.EventType) -> NSEvent {
            NSEvent.mouseEvent(with: kind, location: NSPoint(x: x, y: y), modifierFlags: [],
                timestamp: ProcessInfo.processInfo.systemUptime, windowNumber: window.windowNumber,
                context: nil, eventNumber: 1, clickCount: 1, pressure: 1)!
        }
        NSApp.postEvent(event(.leftMouseUp), atStart: false)
        window.sendEvent(event(.leftMouseDown))
        if let release = NSApp.nextEvent(matching: .leftMouseUp, until: Date(), inMode: .default, dequeue: true) {
            require(release.windowNumber == window.windowNumber, "unexpected test-window mouse release")
            window.sendEvent(release)
        }
        pump()
    }
    static func clickTour(_ id: String, in window: NSWindow) {
        pump(0.25) // Wait for the target relocation animation before hit-testing.
        guard let root = window.contentView, let button = find(NSButton.self, in: root).first(where: { $0.accessibilityIdentifier() == id }) else {
            fatalError("Missing native tour button: " + id)
        }
        let point = button.convert(NSPoint(x: button.bounds.midX, y: button.bounds.midY), to: nil)
        guideClick(window, x: point.x, y: point.y)
    }
    static func tourKey(_ character: String, code: UInt16, in window: NSWindow) {
        let event = NSEvent.keyEvent(with: .keyDown, location: .zero, modifierFlags: [], timestamp: ProcessInfo.processInfo.systemUptime,
            windowNumber: window.windowNumber, context: nil, characters: character, charactersIgnoringModifiers: character,
            isARepeat: false, keyCode: code)!
        if !window.performKeyEquivalent(with: event) { window.sendEvent(event) }
        pump()
    }
    static func gettingStartedChecks() throws {
        let timeout = DispatchWorkItem { Darwin.exit(1) }
        DispatchQueue.global(qos: .utility).asyncAfter(deadline: .now() + 60, execute: timeout)
        defer { timeout.cancel() }
        guard let berth = ProcessInfo.processInfo.environment["BERTH_HOME"], berth.hasPrefix("/") else {
            fatalError("tour checks require isolated BERTH_HOME")
        }
        let suite = "workspace-tour-test-" + UUID().uuidString
        let defaults = UserDefaults(suiteName: suite)!
        defer { defaults.removePersistentDomain(forName: suite) }
        for expectation in ["first", "later"] {
            let child = Process()
            child.executableURL = URL(fileURLWithPath: CommandLine.arguments[0])
            child.arguments = ["--tour-eligibility", suite, expectation]
            try child.run(); child.waitUntilExit()
            require(child.terminationStatus == 0, "tour repeated across an actual process restart")
        }
        defaults.removePersistentDomain(forName: suite)
        require(TourFirstUse.claim(defaults: defaults, legacyUser: false), "fresh user missed first presentation")
        require(!TourFirstUse.claim(defaults: UserDefaults(suiteName: suite)!, legacyUser: false), "second window or restart repeats tour")
        defaults.removePersistentDomain(forName: suite)
        require(!TourFirstUse.claim(defaults: defaults, legacyUser: true), "upgrade unexpectedly replays tutorial")
        require(defaults.bool(forKey: TourFirstUse.key), "legacy migration not persistent")
        defaults.removePersistentDomain(forName: suite)
        for size in [CGSize(width: 760, height: 520), CGSize(width: 900, height: 640), CGSize(width: 1060, height: 720)] {
            for target in [CGRect(x: 12, y: 50, width: 160, height: size.height - 60), CGRect(x: size.width - 28, y: 12, width: 14, height: 14), CGRect(x: 210, y: 90, width: 450, height: 40)] {
                let card = TourLayout.card(near: target, in: size)
                require(CGRect(origin: .zero, size: size).contains(card), "tour card escaped viewport")
                require(!card.intersects(target), "tour card obscured target")
            }
        }
        require(TourLayout.visible(CGRect(x: -100, y: -100, width: 10, height: 10), in: CGSize(width: 760, height: 520)) == nil, "offscreen target is eligible")
        let visualSettings = UISettings.shared
        let priorTheme = visualSettings.previewTheme
        let priorScale = visualSettings.interfaceScale
        visualSettings.previewTheme = ThemeConfiguration()
        defer { visualSettings.previewTheme = priorTheme; visualSettings.interfaceScale = priorScale }
        let before = TerminalSessions.shared.sessions.count
        let navigation = ViewState()
        var measurement: TourMeasurement?
        let content = BoardView(store: BoardStore(fixture: []), services: ServicesStore(fixture: []),
            automaticGuide: true, guideDefaults: defaults, views: navigation)
            .onPreferenceChange(TourMeasurementKey.self) { measurement = $0 }
        let host = NSHostingView(rootView: content)
        host.sizingOptions = [] // The test window, not intrinsic fitting, owns the resize.
        let window = NSWindow(contentRect: NSRect(x: 0, y: 0, width: 1060, height: 720), styleMask: [.titled, .resizable], backing: .buffered, defer: false)
        window.isReleasedWhenClosed = false
        window.contentView = host; window.makeKeyAndOrderFront(nil); pump(0.3)
        defer { window.contentView = nil; window.close() }
        eventually("first-use native tour did not appear") { navigation.showingGuide && measurement != nil }
        require(window.attachedSheet == nil, "tour is still a text sheet")
        require(defaults.bool(forKey: TourFirstUse.key), "presentation did not persist eligibility")
        require(measurement?.target == .connect, "first highlight is not the actual connect control")
        func capture(_ name: String) throws {
            pump(0.25)
            guard let bitmap = host.bitmapImageRepForCachingDisplay(in: host.bounds) else { fatalError("native tour bitmap unavailable") }
            host.cacheDisplay(in: host.bounds, to: bitmap)
            guard let png = bitmap.representation(using: .png, properties: [:]) else { fatalError("native tour PNG unavailable") }
            try png.write(to: URL(fileURLWithPath: berth).appendingPathComponent(name + "-native.png"))
        }
        try capture("guide-empty-paper")
        guard let controls = find(TourControlsView.self, in: host).first else { fatalError("native controls missing") }
        require(window.makeFirstResponder(controls.nextButton), "primary guide control cannot receive keyboard focus")
        window.selectNextKeyView(nil)
        require(window.firstResponder === controls.skipButton, "tour keyboard focus escaped into the workspace")
        window.selectNextKeyView(nil)
        require(window.firstResponder === controls.nextButton, "disabled Back interrupted first-step focus cycle")
        window.selectPreviousKeyView(nil)
        require(window.firstResponder === controls.skipButton, "reverse Tab escaped the tour")
        window.selectPreviousKeyView(nil)
        require(window.firstResponder === controls.nextButton, "reverse focus cycle included disabled Back")

        clickTour("guide.next", in: window)
        eventually("native Next did not select worktrees") { measurement?.target == .worktrees }
        try capture("guide-worktrees-paper")
        clickTour("guide.back", in: window)
        eventually("native Back did not return to connect") { measurement?.target == .connect }
        // Clicking the highlighted real control must not execute through the mask.
        if let hit = measurement?.highlight {
            let point = host.convert(NSPoint(x: hit.midX, y: host.isFlipped ? hit.midY : host.bounds.height - hit.midY), to: nil)
            guideClick(window, x: point.x, y: point.y)
            require(navigation.showingGuide && window.attachedSheet == nil, "spotlight leaked a click into workspace")
        }
        tourKey("\r", code: 36, in: window)
        eventually("Return did not advance native tour") { measurement?.target == .worktrees }
        let oldCard = measurement!.card
        window.setContentSize(NSSize(width: 760, height: 520)); pump(0.35)
        require(measurement!.viewport.width <= 760, "resizing kept stale target coordinates")
        require(measurement!.card != oldCard, "resizing did not reposition the card")
        require(CGRect(origin: .zero, size: measurement!.viewport).contains(measurement!.card), "minimum-window card is clipped")
        require(!measurement!.card.intersects(measurement!.highlight), "minimum-window card covers highlight")
        try capture("guide-minimum-paper")
        tourKey("\u{1b}", code: 53, in: window)
        eventually("Escape did not dismiss native tour") { !navigation.showingGuide }
        window.contentView = nil; pump()
        let second = ViewState()
        window.contentView = NSHostingView(rootView: BoardView(store: BoardStore(fixture: []), services: ServicesStore(fixture: []),
            automaticGuide: true, guideDefaults: UserDefaults(suiteName: suite)!, views: second))
        pump(0.2)
        require(!second.showingGuide, "second presentation ignored durable skip")
        second.showingGuide = true; pump(0.2)
        require(find(TourControlsView.self, in: window.contentView!).count == 1, "manual replay unavailable")
        for _ in 0..<6 { clickTour("guide.next", in: window) }
        require(!second.showingGuide, "Done did not close tour or missing targets were not skipped")
        second.showingGuide = true; pump(0.2)
        clickTour("guide.skip", in: window)
        require(!second.showingGuide, "native Skip did not close tour")
        window.contentView = nil; pump()

        // A synthetic project is injected into the actual BoardView. No daemon,
        // Git subprocess or created on-disk project is required for this check.
        let project = try JSONDecoder().decode(BerthGroup.self, from: Data(#"{"name":"demo@feature","repo":"demo","worktree":"feature","branch":"feature/onboarding","root_dir":"/fixture/demo","config_path":"/fixture/demo/oberth.yaml","services":[],"members":[]}"#.utf8))
        require(TourMotion.animation(reduced: true) == nil && TourMotion.animation(reduced: false) != nil, "Reduce Motion policy ignored")
        visualSettings.previewTheme = try ClientConfigurationIO.decode(ThemeConfiguration.self, data: Data(##"{"schemaVersion":1,"appearance":"dark","colors":{"canvas":"#16191E","surface":"#1F242B","ink":"#E8EBF0","inkMuted":"#B8C0CC","inkFaint":"#8B96A5","line":"#343B46","accent":"#8CB3FF","accentSoft":"#283B59"}}"##.utf8))
        let loaded = ViewState(scope: .services(project.name))
        var loadedMeasurement: TourMeasurement?
        let loadedHost = NSHostingView(rootView: BoardView(store: BoardStore(fixture: []),
            services: ServicesStore(fixture: [project]), git: GitStore(overview: nil), views: loaded)
            .onPreferenceChange(TourMeasurementKey.self) { loadedMeasurement = $0 })
        loadedHost.sizingOptions = []
        window.contentView = loadedHost; window.setContentSize(NSSize(width: 900, height: 640)); pump(0.2)
        loaded.showingGuide = true; pump(0.2)
        clickTour("guide.next", in: window)
        clickTour("guide.next", in: window)
        eventually("existing-project tabs did not receive a real anchor") { loadedMeasurement?.target == .facts }
        require(window.attachedSheet == nil, "project guidance unexpectedly uses a sheet")
        require(CGRect(origin: .zero, size: loadedMeasurement!.viewport).contains(loadedMeasurement!.card), "dark guide card escaped viewport")
        require(!loadedMeasurement!.card.intersects(loadedMeasurement!.highlight), "dark card obscures project tabs")
        pump(0.3) // Capture the settled page, not an intermediate animation frame.
        guard let image = loadedHost.bitmapImageRepForCachingDisplay(in: loadedHost.bounds) else { fatalError("dark native bitmap unavailable") }
        loadedHost.cacheDisplay(in: loadedHost.bounds, to: image)
        try image.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: berth).appendingPathComponent("guide-project-midnight-native.png"))
        loaded.guideTarget = .commands; pump(0.06)
        guard let transition = loadedHost.bitmapImageRepForCachingDisplay(in: loadedHost.bounds) else { fatalError("transition bitmap unavailable") }
        loadedHost.cacheDisplay(in: loadedHost.bounds, to: transition)
        try transition.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: berth).appendingPathComponent("guide-transition-midnight-native.png"))
        pump(0.2)
        loaded.guideTarget = .worktrees; pump(0.25)
        tourKey("\u{f702}", code: 123, in: window)
        eventually("Left arrow did not navigate back") { loadedMeasurement?.target == .connect }
        tourKey("\u{f703}", code: 124, in: window)
        eventually("Right arrow did not navigate forward") { loadedMeasurement?.target == .worktrees }
        visualSettings.interfaceScale = 1.3
        loaded.guideTarget = .recovery; pump(0.3)
        guard let scaled = loadedHost.bitmapImageRepForCachingDisplay(in: loadedHost.bounds) else { fatalError("scaled native bitmap unavailable") }
        loadedHost.cacheDisplay(in: loadedHost.bounds, to: scaled)
        try scaled.representation(using: .png, properties: [:])!.write(to: URL(fileURLWithPath: berth).appendingPathComponent("guide-scaled-midnight-native.png"))
        guard let scaledControls = find(TourControlsView.self, in: loadedHost).first else { fatalError("scaled guide controls missing") }
        let actionPoint = scaledControls.nextButton.convert(NSPoint(x: scaledControls.nextButton.bounds.midX, y: scaledControls.nextButton.bounds.midY), to: loadedHost)
        require(loadedHost.bounds.contains(actionPoint), "large fonts moved controls outside the window")
        visualSettings.interfaceScale = priorScale
        loaded.guideTarget = .worktrees; pump(0.25)
        loaded.railVisible = false; pump()
        require(loadedMeasurement?.target != .worktrees, "missing sidebar retained a floating spotlight")
        clickTour("guide.skip", in: window)
        require(!loaded.showingGuide, "dark native Skip did not dismiss")
        // Native focus ownership is returned only to a live view in this window.
        let focusRoot = NSView(frame: NSRect(x: 0, y: 0, width: 500, height: 100))
        let editor = NSTextView(frame: NSRect(x: 0, y: 0, width: 100, height: 40))
        focusRoot.addSubview(editor); window.contentView = focusRoot
        window.makeFirstResponder(editor)
        let focusControls = TourControlsView()
        focusControls.frame = NSRect(x: 0, y: 50, width: 330, height: 28)
        focusRoot.addSubview(focusControls)
        require(window.firstResponder === focusControls.nextButton, "guide did not claim native keyboard focus")
        focusControls.restoreFocus()
        require(window.firstResponder === editor, "guide failed to restore the previous live editor")

        require(TerminalSessions.shared.sessions.count == before, "tour created a shell or agent")
        require(WorkspaceShortcuts.valid([:]), "tour changed editing/window shortcuts")
        for step in TourTarget.allCases { require(!step.chinese.isEmpty && !step.english.isEmpty, "missing bilingual guidance") }
        print("PASS: first-use/upgrade/skip/actual process restart/manual replay; native Next/Back/Return/Escape/arrows/Done, focus loop/restore, real project tabs, dark/reduce-motion, resize and no processes")
    }
}
