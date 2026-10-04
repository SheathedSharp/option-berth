import Foundation

/// Temporary, opt-in zsh startup files. No edits to the person's shell profiles.
/// The lease remains alive until the owned shell exits, then removes its files.
public final class ShellIntegrationLease {
    public let directory: URL
    public let nonce: String
    public let environment: [String: String]
    public init(environment original: [String: String], directory parent: URL = FileManager.default.temporaryDirectory) throws {
        let nonce = UUID().uuidString.replacingOccurrences(of: "-", with: "")
        let directory = parent.appendingPathComponent("oberth-zsh-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        self.directory = directory; self.nonce = nonce
        var env = original
        let home = env["HOME"] ?? FileManager.default.homeDirectoryForCurrentUser.path
        guard home.hasPrefix("/"), (env["ZDOTDIR"] ?? home).hasPrefix("/") else {
            try? FileManager.default.removeItem(at: directory)
            throw CocoaError(.fileReadInvalidFileName)
        }
        env["OBERTH_ORIGINAL_ZDOTDIR"] = env["ZDOTDIR"] ?? home
        env["ZDOTDIR"] = directory.path
        self.environment = env
        // The original .zshenv may choose another ZDOTDIR. Preserve that choice
        // while redirecting only this shell's .zshrc through the temporary file.
        let zshenv = #"""
        typeset -g __oberth_loader_dir=$ZDOTDIR
        ZDOTDIR=$OBERTH_ORIGINAL_ZDOTDIR
        [[ -r "$ZDOTDIR/.zshenv" ]] && source "$ZDOTDIR/.zshenv"
        typeset -g __oberth_user_dir=${ZDOTDIR:-$HOME}
        ZDOTDIR=$__oberth_loader_dir
        """#
        let zshrc = #"""
        ZDOTDIR=$__oberth_user_dir
        [[ -r "$ZDOTDIR/.zshrc" ]] && source "$ZDOTDIR/.zshrc"
        unset OBERTH_ORIGINAL_ZDOTDIR
        autoload -Uz add-zsh-hook
        typeset -g __oberth_running=0
        __oberth_preexec() {
          local command="$1"
          if (( ${#command} <= 8192 )); then
            command=${command//\\/\\\\}
            command=${command//;/\\x3b}
            command=${command//$'\n'/\\x0a}
            command=${command//$'\r'/\\x0d}
            command=${command//$'\e'/\\x1b}
            command=${command//$'\a'/\\x07}
            printf '\e]633;E;%s;__NONCE__\a' "$command"
          fi
          printf '\e]133;C;__NONCE__\a'
          __oberth_running=1
        }
        __oberth_precmd() {
          local result=$?
          if (( __oberth_running )); then
            printf '\e]133;D;%d;__NONCE__\a' "$result"
          fi
          __oberth_running=0
          printf '\e]133;A;__NONCE__\a'
          return $result
        }
        add-zsh-hook preexec __oberth_preexec
        add-zsh-hook precmd __oberth_precmd
        # Observe the status before other array hooks mutate it; return it intact.
        precmd_functions=(__oberth_precmd ${precmd_functions:#__oberth_precmd})
        """#.replacingOccurrences(of: "__NONCE__", with: nonce)
        do {
            for (name, text) in [(".zshenv", zshenv), (".zshrc", zshrc)] {
                let path = directory.appendingPathComponent(name)
                guard FileManager.default.createFile(atPath: path.path, contents: Data((text + "\n").utf8), attributes: [.posixPermissions: 0o600]) else {
                    throw CocoaError(.fileWriteUnknown)
                }
            }
        } catch { try? FileManager.default.removeItem(at: directory); throw error }
    }
    deinit { try? FileManager.default.removeItem(at: directory) }
}
