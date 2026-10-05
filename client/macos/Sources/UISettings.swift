import AppKit
import SwiftUI

/// Paper defaults + explicit user files. Existing GUI preferences are a legacy
/// fallback; user-file fields take precedence and are never overwritten by UI.
final class UISettings: ObservableObject {
    static let shared: UISettings = {
        let args = CommandLine.arguments
        if args.contains("--render-states") || args.contains("--write-icon") {
            if args.contains("--render-theme") {
                fputs("Use --render-theme-file instead of a retired built-in theme name\n", stderr); exit(2)
            }
            let isolated = UserDefaults(suiteName: "option-berth-render-" + UUID().uuidString)!
            let value = UISettings(defaults: isolated)
            if let index = args.firstIndex(of: "--render-theme-file") {
                guard index + 1 < args.count else { fputs("Missing --render-theme-file argument\n", stderr); exit(2) }
                do {
                    guard let data = try ClientConfigurationIO.read(URL(fileURLWithPath: args[index + 1])) else {
                        throw ClientConfigurationError.invalid("render theme does not exist")
                    }
                    value.previewTheme = try ClientConfigurationIO.decode(ThemeConfiguration.self, data: data)
                } catch {
                    // An explicit fixture must not silently render a different theme.
                    fputs("Invalid --render-theme-file\n", stderr); exit(2)
                }
            }
            return value
        }
        return UISettings(configurationDirectory: ClientConfigurationIO.directory())
    }()

    @Published private(set) var configuration = ClientConfigurationSnapshot()
    @Published var previewTheme: ThemeConfiguration?
    @Published private var legacy = ThemeConfiguration()
    private let defaults: UserDefaults
    private var monitor: ClientConfigurationMonitor?
    let configurationDirectory: URL?

    init(defaults: UserDefaults = .standard, configurationDirectory: URL? = nil) {
        self.defaults = defaults
        self.configurationDirectory = configurationDirectory
        legacy.interfaceFont = defaults.string(forKey: "ui.interfaceFont")
        legacy.dataFont = defaults.string(forKey: "ui.dataFont")
        func scale(_ key: String) -> Double? {
            guard let value = defaults.object(forKey: key) as? Double,
                  value.isFinite, (0.75...2).contains(value) else { return nil }
            return value
        }
        legacy.interfaceScale = scale("ui.interfaceScale")
        legacy.dataScale = scale("ui.dataScale")
        legacy.logScale = scale("ui.logScale")
        if let raw = defaults.string(forKey: "ui.accent"), let value = Self.parseHex(raw) {
            legacy.colors = ["accent": String(format: "#%06X", value)]
        }
        // Retired theme names deliberately do not select another built-in palette.
        if let configurationDirectory {
            let observer = ClientConfigurationMonitor(directory: configurationDirectory) { [weak self] in self?.configuration = $0 }
            configuration = observer.initial; monitor = observer
        }
    }
    func reloadConfiguration() { monitor?.reload() }
    private var theme: ThemeConfiguration { previewTheme ?? configuration.theme }
    var themeFilePresent: Bool { configuration.themeFilePresent }
    var colorScheme: ColorScheme { theme.appearance == "dark" ? .dark : .light }
    var interfaceFontName: String {
        get { theme.interfaceFont ?? legacy.interfaceFont ?? "__system__" }
        set { legacy.interfaceFont = newValue; defaults.set(newValue, forKey: "ui.interfaceFont") }
    }
    var dataFontName: String {
        get { theme.dataFont ?? legacy.dataFont ?? "Monaspace Neon" }
        set { legacy.dataFont = newValue; defaults.set(newValue, forKey: "ui.dataFont") }
    }
    var interfaceScale: Double {
        get { theme.interfaceScale ?? legacy.interfaceScale ?? 1 }
        set { legacy.interfaceScale = newValue; defaults.set(newValue, forKey: "ui.interfaceScale") }
    }
    var dataScale: Double {
        get { theme.dataScale ?? legacy.dataScale ?? 1 }
        set { legacy.dataScale = newValue; defaults.set(newValue, forKey: "ui.dataScale") }
    }
    var logScale: Double {
        get { theme.logScale ?? legacy.logScale ?? 1 }
        set { legacy.logScale = newValue; defaults.set(newValue, forKey: "ui.logScale") }
    }
    var accentHex: String? { theme.colors?["accent"] ?? legacy.colors?["accent"] }
    func token(_ name: String) -> Color {
        if let value = theme.colors?[name].flatMap(ThemeConfiguration.hex) { return Color(hex: value) }
        if name == "accent", let value = legacy.colors?[name].flatMap(ThemeConfiguration.hex) { return Color(hex: value) }
        return Color(hex: ThemeConfiguration.paper[name] ?? ThemeConfiguration.paper["ink"]!)
    }
    var canvasColor: Color { token("canvas") }
    var surfaceColor: Color { token("surface") }
    var sunkenColor: Color { token("sunken") }
    var inkColor: Color { token("ink") }
    var inkMutedColor: Color { token("inkMuted") }
    var inkFaintColor: Color { token("inkFaint") }
    var dormantColor: Color { token("dormant") }
    var lineColor: Color { token("line") }
    var lineStrongColor: Color { token("lineStrong") }
    var accentColor: Color { token("accent") }
    var accentSoftColor: Color {
        if theme.colors?["accentSoft"] != nil { return token("accentSoft") }
        return accentHex == nil ? token("accentSoft") : accentColor.opacity(colorScheme == .dark ? 0.22 : 0.10)
    }
    var liveColor: Color { token("live") }
    var terminalBackground: Color { theme.colors?["terminal.background"] == nil ? canvasColor : token("terminal.background") }
    var terminalForeground: Color { theme.colors?["terminal.foreground"] == nil ? inkColor : token("terminal.foreground") }
    func setAccent(_ color: Color) {
        guard let c = NSColor(color).usingColorSpace(.sRGB) else { return }
        let value = String(format: "#%02X%02X%02X", Int((c.redComponent * 255).rounded()), Int((c.greenComponent * 255).rounded()), Int((c.blueComponent * 255).rounded()))
        legacy.colors = ["accent": value]; defaults.set(value, forKey: "ui.accent")
    }
    func resetAccent() { legacy.colors = nil; defaults.removeObject(forKey: "ui.accent") }
    func reset() {
        legacy = ThemeConfiguration()
        for key in ["ui.theme", "ui.interfaceFont", "ui.dataFont", "ui.interfaceScale", "ui.dataScale", "ui.logScale", "ui.accent"] {
            defaults.removeObject(forKey: key)
        }
    }
    var availableFontFamilies: [String] {
        _ = FontBook.monoAvailable
        return ["__system__", "Monaspace Neon"] + NSFontManager.shared.availableFontFamilies
            .filter { !$0.hasPrefix(".") && $0 != "Monaspace Neon" }.sorted()
    }
    static func parseHex(_ text: String) -> UInt32? {
        let raw = text.trimmingCharacters(in: .whitespacesAndNewlines)
        return ThemeConfiguration.hex(raw.hasPrefix("#") ? raw : "#" + raw)
    }
}
