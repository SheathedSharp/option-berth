// swift-tools-version: 6.0
import PackageDescription

let package = Package(
    name: "OptionBerth",
    platforms: [.macOS(.v14)],
    products: [.executable(name: "OptionBerth", targets: ["OptionBerth"]),
               .executable(name: "TerminalChecks", targets: ["TerminalChecks"]),
               .executable(name: "AgentChecks", targets: ["AgentChecks"]),
               .executable(name: "WorkspaceChecks", targets: ["WorkspaceChecks"]),
               .executable(name: "ClientChecks", targets: ["ClientChecks"])],
    dependencies: [
        .package(url: "https://github.com/migueldeicaza/SwiftTerm.git",
                 revision: "5d14406844143538cd8f8851d2d8a67c1fe443e5")
    ],
    targets: [
        .target(name: "BerthTerminal", dependencies: ["SwiftTerm"], path: "TerminalSupport"),
        .target(name: "BerthAgent", path: "AgentSupport"),
        .target(name: "BerthClient", dependencies: ["BerthTerminal", "BerthAgent"], path: "Sources"),
        .executableTarget(name: "OptionBerth", dependencies: ["BerthClient"], path: "Application"),
        .executableTarget(name: "TerminalChecks", dependencies: ["BerthTerminal"], path: "TerminalTests"),
        .executableTarget(name: "ClientChecks", dependencies: ["BerthClient", "BerthTerminal", "BerthAgent"], path: "ClientTests"),
        .executableTarget(name: "WorkspaceChecks", dependencies: ["BerthTerminal"], path: "WorkspaceTests"),
        .executableTarget(name: "AgentChecks", dependencies: ["BerthAgent", "BerthTerminal"], path: "AgentTests")
    ],
    swiftLanguageModes: [.v5]
)
