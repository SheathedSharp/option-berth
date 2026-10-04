import Foundation
import Darwin

@main
struct DaemonLaunchTests {
    static func main() {
        let home = "/fixture/home"
        let custom = "/fixture/工具 bin"
        var checks = 0
        func check(_ name: String, _ env: [String: String], _ executables: Set<String>, _ expected: String?) {
            let result = DaemonLaunch.binaryPath(environment: env, home: home, executableURL: nil,
                isExecutable: { executables.contains($0) })
            guard result == expected else {
                FileHandle.standardError.write(Data("FAIL \(name): got \(String(describing: result)), expected \(String(describing: expected))\n".utf8))
                exit(1)
            }
            checks += 1
        }
        check("canonical CLI on custom PATH", ["PATH": custom], [custom + "/oberth"], custom + "/oberth")
        check("override wins", ["PATH": custom, "BERTH_BIN": "/fixture/pinned"],
              ["/fixture/pinned", custom + "/oberth"], "/fixture/pinned")
        check("missing override falls back", ["PATH": custom, "BERTH_BIN": "/missing"],
              [custom + "/oberth"], custom + "/oberth")
        check("user installation wins", ["PATH": custom],
              [home + "/.local/bin/oberth", custom + "/oberth"], home + "/.local/bin/oberth")
        check("PATH ordering", ["PATH": "/first:" + custom],
              ["/first/oberth", custom + "/oberth"], "/first/oberth")
        check("legacy command is not canonical", ["PATH": custom], [custom + "/option-berth"], nil)
        check("no implicit current directory", ["PATH": ":"], ["./oberth", "/oberth"], nil)
        check("missing executables", ["PATH": custom], [], nil)
        let app = URL(fileURLWithPath: "/fixture/OptionBerth.app/Contents/MacOS/OptionBerth")
        let bundled = "/fixture/OptionBerth.app/Contents/MacOS/oberth"
        for override in [false, true] {
            let result = DaemonLaunch.binaryPath(environment: override ? ["BERTH_BIN": "/fixture/pinned"] : [:],
                home: home, executableURL: app,
                isExecutable: { [bundled, home + "/.local/bin/oberth", "/fixture/pinned"].contains($0) })
            guard result == (override ? "/fixture/pinned" : bundled) else {
                FileHandle.standardError.write(Data("FAIL matching bundled engine precedence\n".utf8))
                exit(1)
            }
            checks += 1
        }
        print("DaemonLaunch executable resolution: \(checks) checks passed")
    }
}
