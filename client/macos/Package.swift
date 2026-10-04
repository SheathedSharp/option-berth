// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "OptionBerth",
    platforms: [.macOS(.v14)],
    products: [.executable(name: "OptionBerth", targets: ["OptionBerth"]),
               .executable(name: "TerminalChecks", targets: ["TerminalChecks"])],
    dependencies: [
        .package(url: "https://github.com/migueldeicaza/SwiftTerm.git",
                 revision: "5d14406844143538cd8f8851d2d8a67c1fe443e5")
    ],
    targets: [
        .target(name: "BerthTerminal", dependencies: ["SwiftTerm"], path: "TerminalSupport"),
        .executableTarget(name: "OptionBerth", dependencies: ["BerthTerminal"], path: "Sources"),
        .executableTarget(name: "TerminalChecks", dependencies: ["BerthTerminal"], path: "TerminalTests")
    ],
    swiftLanguageModes: [.v5]
)
