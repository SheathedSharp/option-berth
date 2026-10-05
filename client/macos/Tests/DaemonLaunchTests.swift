import Foundation
import Darwin

@main
struct DaemonLaunchTests {
    static func main() throws {
        let home = "/fixture/home"
        let custom = "/fixture/工具 bin"
        var checks = 0
        func require(_ condition: Bool, _ name: String) {
            guard condition else {
                FileHandle.standardError.write(Data("FAIL \(name)\n".utf8))
                exit(1)
            }
            checks += 1
        }
        func check(_ name: String, _ env: [String: String], _ executables: Set<String>, _ expected: String?) {
            let result = DaemonLaunch.binaryPath(environment: env, home: home, executableURL: nil,
                isExecutable: { executables.contains($0) })
            require(result == expected, name)
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
            require(result == (override ? "/fixture/pinned" : bundled), "matching bundled engine precedence")
        }

        // Short, owned paths fit sockaddr_un on Darwin. Never touch the user's socket.
        let root = URL(fileURLWithPath: "/tmp/oberth-launch-\(UUID().uuidString)")
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true,
                                                attributes: [.posixPermissions: 0o700])
        defer { try? FileManager.default.removeItem(at: root) }
        let previous = ProcessInfo.processInfo.environment["BERTH_NO_AUTOSTART"]
        setenv("BERTH_NO_AUTOSTART", "1", 1)
        defer {
            if let previous { setenv("BERTH_NO_AUTOSTART", previous, 1) }
            else { unsetenv("BERTH_NO_AUTOSTART") }
        }
        let path = root.appendingPathComponent("daemon.sock").path
        let fd = socket(AF_UNIX, SOCK_STREAM, 0)
        require(fd >= 0, "create owned Unix socket")
        var address = sockaddr_un()
        address.sun_family = sa_family_t(AF_UNIX)
        let capacity = MemoryLayout.size(ofValue: address.sun_path)
        withUnsafeMutablePointer(to: &address.sun_path) { slot in
            slot.withMemoryRebound(to: CChar.self, capacity: capacity) { destination in
                _ = path.withCString { strcpy(destination, $0) }
            }
        }
        let bound = withUnsafePointer(to: &address) { pointer in
            pointer.withMemoryRebound(to: sockaddr.self, capacity: 1) {
                Darwin.bind(fd, $0, socklen_t(MemoryLayout<sockaddr_un>.size))
            }
        }
        require(bound == 0, "bind owned Unix socket")
        require(listen(fd, 8) == 0, "listen on owned Unix socket")
        try DaemonLaunch.ensureRunning(socketPath: path)
        checks += 1
        close(fd)
        require(FileManager.default.fileExists(atPath: path), "stale socket fixture exists")
        var rejected = false
        do { try DaemonLaunch.ensureRunning(socketPath: path) } catch { rejected = true }
        require(rejected, "stale socket is not readiness when autostart is forbidden")
        require(FileManager.default.fileExists(atPath: path), "client does not unlink the engine socket")
        print("DaemonLaunch: \(checks) checks passed")
    }
}
