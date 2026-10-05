import Foundation
import Darwin

struct ClientConfigurationSnapshot: Equatable {
    var theme = ThemeConfiguration()
    var preferences = ClientPreferencesConfiguration()
    var keybindings = ClientKeybindingsConfiguration()
    var problems: [String] = []
    var themeFilePresent = false
    var keybindingsFilePresent = false
}

/// Directory events survive an editor's atomic rename. No polling timer, file
/// writes or process launches. Reads are bounded and run on one utility queue.
final class ClientConfigurationMonitor {
    let initial: ClientConfigurationSnapshot
    private let directory: URL
    private let theme: ClientConfigurationFile<ThemeConfiguration>
    private let preferences: ClientConfigurationFile<ClientPreferencesConfiguration>
    private let keybindings: ClientConfigurationFile<ClientKeybindingsConfiguration>
    private let queue = DispatchQueue(label: "option-berth.client-configuration", qos: .utility)
    private let changed: (ClientConfigurationSnapshot) -> Void
    private var sources: [DispatchSourceFileSystemObject] = []
    private var scheduled: DispatchWorkItem?
    private var last: ClientConfigurationSnapshot

    init(directory: URL, changed: @escaping (ClientConfigurationSnapshot) -> Void) {
        self.directory = directory.standardizedFileURL
        self.changed = changed
        theme = ClientConfigurationFile(url: directory.appendingPathComponent("theme.json"))
        preferences = ClientConfigurationFile(url: directory.appendingPathComponent("settings.json"))
        keybindings = ClientConfigurationFile(url: directory.appendingPathComponent("keybindings.json"))
        theme.reload(); preferences.reload(); keybindings.reload()
        let value = Self.snapshot(theme, preferences, keybindings)
        initial = value; last = value
        queue.async { [weak self] in self?.arm(); self?.read() }
    }
    deinit { scheduled?.cancel(); sources.forEach { $0.cancel() } }

    func reload() { queue.async { [weak self] in self?.read() } }

    private static func snapshot(_ theme: ClientConfigurationFile<ThemeConfiguration>,
                                 _ preferences: ClientConfigurationFile<ClientPreferencesConfiguration>,
                                 _ keybindings: ClientConfigurationFile<ClientKeybindingsConfiguration>) -> ClientConfigurationSnapshot {
        ClientConfigurationSnapshot(theme: theme.value, preferences: preferences.value, keybindings: keybindings.value,
            problems: [theme.problem, preferences.problem, keybindings.problem].compactMap { $0 },
            themeFilePresent: theme.exists || theme.problem != nil,
            keybindingsFilePresent: keybindings.exists || keybindings.problem != nil)
    }
    private func read() {
        theme.reload(); preferences.reload(); keybindings.reload()
        let value = Self.snapshot(theme, preferences, keybindings)
        guard value != last else { return }
        last = value
        DispatchQueue.main.async { [weak self] in self?.changed(value) }
    }
    private func schedule() {
        // Coalesce without indefinitely postponing reloads in a busy directory.
        guard scheduled == nil else { return }
        let work = DispatchWorkItem { [weak self] in
            guard let self else { return }
            self.scheduled = nil; self.arm(); self.read()
        }
        scheduled = work
        queue.asyncAfter(deadline: .now() + 0.12, execute: work)
    }
    private func arm() {
        sources.forEach { $0.cancel() }; sources.removeAll()
        var candidate = directory
        while !FileManager.default.fileExists(atPath: candidate.path), candidate.path != "/" {
            candidate.deleteLastPathComponent()
        }
        // Parent catches removal/replacement of the watched directory itself.
        let paths = Set([candidate.path, candidate.deletingLastPathComponent().path, theme.url.path, preferences.url.path, keybindings.url.path])
        for path in paths {
            let fd = open(path, O_EVTONLY | O_CLOEXEC | O_NOFOLLOW)
            guard fd >= 0 else { continue }
            let source = DispatchSource.makeFileSystemObjectSource(fileDescriptor: fd,
                eventMask: [.write, .extend, .rename, .delete, .attrib, .revoke], queue: queue)
            source.setEventHandler { [weak self] in self?.schedule() }
            source.setCancelHandler { close(fd) }
            sources.append(source); source.resume()
        }
    }
}
