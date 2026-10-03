import AppKit
import Combine
import SwiftUI

/// The visual preferences used by every surface in the client.
///
/// Preferences live in UserDefaults because they describe this local client,
/// not the daemon or a worktree manifest. The singleton keeps the existing
/// token based views lightweight while the published properties make changes
/// in the settings sheet take effect immediately.
final class UISettings: ObservableObject {
    static let shared = UISettings()

    enum Theme: String, CaseIterable, Identifiable {
        case glacier
        case midnight
        case paper
        case forest

        var id: String { rawValue }

        var title: String {
            switch self {
            case .glacier: return "冰川"
            case .midnight: return "午夜"
            case .paper: return "纸张"
            case .forest: return "森林"
            }
        }

        var subtitle: String {
            switch self {
            case .glacier: return "冷白与蓝色"
            case .midnight: return "深色工作台"
            case .paper: return "暖纸与墨色"
            case .forest: return "深绿与苔色"
            }
        }

        var swatches: [Color] {
            let palette = palette
            return [palette.canvasColor, palette.surfaceColor, palette.accentColor]
        }

        fileprivate var palette: ThemePalette {
            switch self {
            case .glacier:
                return ThemePalette(canvas: 0xFFFFFF, surface: 0xF6F8FC, sunken: 0xEDF1F8,
                                    ink: 0x0B1220, inkMuted: 0x5A6577, inkFaint: 0x8D97A8,
                                    dormant: 0xB9C1CE, line: 0xE2E7EF, lineStrong: 0xCBD4E0,
                                    accent: 0x1E4BD8, accentSoft: 0xEEF3FE, live: 0x2C6BF0)
            case .midnight:
                return ThemePalette(canvas: 0x11151D, surface: 0x181E28, sunken: 0x222A37,
                                    ink: 0xF0F4FA, inkMuted: 0xB5C0D0, inkFaint: 0x7E8BA0,
                                    dormant: 0x586477, line: 0x2B3544, lineStrong: 0x3C485A,
                                    accent: 0x79A7FF, accentSoft: 0x243A62, live: 0x70D6FF)
            case .paper:
                return ThemePalette(canvas: 0xF8F4EE, surface: 0xEFE7DC, sunken: 0xE5DACD,
                                    ink: 0x292521, inkMuted: 0x6B6259, inkFaint: 0x968A7D,
                                    dormant: 0xC5B8A9, line: 0xDED3C7, lineStrong: 0xC9B9A8,
                                    accent: 0xC56A4A, accentSoft: 0xF4DED3, live: 0x5E7D68)
            case .forest:
                return ThemePalette(canvas: 0x101915, surface: 0x17241D, sunken: 0x213328,
                                    ink: 0xE8F1E8, inkMuted: 0xB2C4B4, inkFaint: 0x819684,
                                    dormant: 0x506554, line: 0x2B4032, lineStrong: 0x3C5543,
                                    accent: 0x8ACB88, accentSoft: 0x23452E, live: 0xB7E36E)
            }
        }
    }

    fileprivate struct ThemePalette {
        let canvas: UInt32
        let surface: UInt32
        let sunken: UInt32
        let ink: UInt32
        let inkMuted: UInt32
        let inkFaint: UInt32
        let dormant: UInt32
        let line: UInt32
        let lineStrong: UInt32
        let accent: UInt32
        let accentSoft: UInt32
        let live: UInt32

        var canvasColor: Color { Color(hex: canvas) }
        var surfaceColor: Color { Color(hex: surface) }
        var sunkenColor: Color { Color(hex: sunken) }
        var inkColor: Color { Color(hex: ink) }
        var inkMutedColor: Color { Color(hex: inkMuted) }
        var inkFaintColor: Color { Color(hex: inkFaint) }
        var dormantColor: Color { Color(hex: dormant) }
        var lineColor: Color { Color(hex: line) }
        var lineStrongColor: Color { Color(hex: lineStrong) }
        var accentColor: Color { Color(hex: accent) }
        var accentSoftColor: Color { Color(hex: accentSoft) }
        var liveColor: Color { Color(hex: live) }
    }

    @Published var theme: Theme {
        didSet { defaults.set(theme.rawValue, forKey: Keys.theme) }
    }
    @Published var interfaceFontName: String {
        didSet { defaults.set(interfaceFontName, forKey: Keys.interfaceFont) }
    }
    @Published var dataFontName: String {
        didSet { defaults.set(dataFontName, forKey: Keys.dataFont) }
    }
    @Published var interfaceScale: Double {
        didSet { defaults.set(interfaceScale, forKey: Keys.interfaceScale) }
    }
    @Published var dataScale: Double {
        didSet { defaults.set(dataScale, forKey: Keys.dataScale) }
    }
    @Published var logScale: Double {
        didSet { defaults.set(logScale, forKey: Keys.logScale) }
    }
    @Published var accentHex: String? {
        didSet {
            if let accentHex {
                defaults.set(accentHex, forKey: Keys.accent)
            } else {
                defaults.removeObject(forKey: Keys.accent)
            }
        }
    }

    private let defaults: UserDefaults

    private enum Keys {
        static let theme = "ui.theme"
        static let interfaceFont = "ui.interfaceFont"
        static let dataFont = "ui.dataFont"
        static let interfaceScale = "ui.interfaceScale"
        static let dataScale = "ui.dataScale"
        static let logScale = "ui.logScale"
        static let accent = "ui.accent"
    }

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        // Paper is the calmest reading surface for a service board on first launch.
        theme = Theme(rawValue: defaults.string(forKey: Keys.theme) ?? "") ?? .paper
        interfaceFontName = defaults.string(forKey: Keys.interfaceFont) ?? "__system__"
        dataFontName = defaults.string(forKey: Keys.dataFont) ?? "Monaspace Neon"
        interfaceScale = defaults.object(forKey: Keys.interfaceScale) as? Double ?? 1.0
        dataScale = defaults.object(forKey: Keys.dataScale) as? Double ?? 1.0
        logScale = defaults.object(forKey: Keys.logScale) as? Double ?? 1.0
        accentHex = defaults.string(forKey: Keys.accent)
    }

    fileprivate var palette: ThemePalette { theme.palette }

    var canvasColor: Color { palette.canvasColor }
    var surfaceColor: Color { palette.surfaceColor }
    var sunkenColor: Color { palette.sunkenColor }
    var inkColor: Color { palette.inkColor }
    var inkMutedColor: Color { palette.inkMutedColor }
    var inkFaintColor: Color { palette.inkFaintColor }
    var dormantColor: Color { palette.dormantColor }
    var lineColor: Color { palette.lineColor }
    var lineStrongColor: Color { palette.lineStrongColor }
    var accentSoftColor: Color { palette.accentSoftColor }
    var liveColor: Color { palette.liveColor }

    var accentColor: Color {
        guard let accentHex, let value = Self.parseHex(accentHex) else {
            return palette.accentColor
        }
        return Color(hex: value)
    }

    func setAccent(_ color: Color) {
        let native = NSColor(color).usingColorSpace(.sRGB) ?? NSColor.white
        let red = Int((native.redComponent * 255).rounded())
        let green = Int((native.greenComponent * 255).rounded())
        let blue = Int((native.blueComponent * 255).rounded())
        accentHex = String(format: "#%02X%02X%02X", red, green, blue)
    }

    func resetAccent() {
        accentHex = nil
    }

    func reset() {
        theme = .paper
        interfaceFontName = "__system__"
        dataFontName = "Monaspace Neon"
        interfaceScale = 1.0
        dataScale = 1.0
        logScale = 1.0
        accentHex = nil
    }

    var availableFontFamilies: [String] {
        _ = FontBook.monoAvailable
        let families = NSFontManager.shared.availableFontFamilies
            .filter { !$0.hasPrefix(".Apple") && !$0.hasPrefix(".SF UI") }
            .sorted { $0.localizedCaseInsensitiveCompare($1) == .orderedAscending }
        return ["__system__", "Monaspace Neon"] + families.filter { $0 != "Monaspace Neon" }
    }

    var interfaceFontTitle: String {
        interfaceFontName == "__system__" ? "系统字体" : interfaceFontName
    }

    var dataFontTitle: String {
        dataFontName == "Monaspace Neon" ? "Monaspace Neon" : dataFontName
    }

    static func parseHex(_ string: String) -> UInt32? {
        let normalized = string.trimmingCharacters(in: .whitespacesAndNewlines)
            .replacingOccurrences(of: "#", with: "")
        guard normalized.count == 6 else { return nil }
        return UInt32(normalized, radix: 16)
    }
}
