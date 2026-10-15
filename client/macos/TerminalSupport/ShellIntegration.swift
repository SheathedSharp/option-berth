import Foundation

public enum IntegratedShell: String, Equatable {
    case zsh, bash, fish
}

/// Temporary, opt-in shell startup files. No edits to the person's shell profiles.
/// The lease remains alive until the owned shell exits, then removes its files.
public final class ShellIntegrationLease {
    public let directory: URL
    public let nonce: String
    public let shell: IntegratedShell
    public let environment: [String: String]
    public let launchArguments: [String]

    public static func integratedShell(executable: String) -> IntegratedShell? {
        switch URL(fileURLWithPath: executable).lastPathComponent {
        case "zsh": return .zsh
        case "bash": return .bash
        case "fish": return .fish
        default: return nil
        }
    }

    public static func supports(executable: String) -> Bool { integratedShell(executable: executable) != nil }

    public init(executable: String, environment original: [String: String],
                directory parent: URL = FileManager.default.temporaryDirectory) throws {
        guard let shell = Self.integratedShell(executable: executable) else {
            throw CocoaError(.fileReadNoSuchFile)
        }
        let nonce = UUID().uuidString.replacingOccurrences(of: "-", with: "")
        let directory = parent.appendingPathComponent("oberth-\(shell.rawValue)-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false, attributes: [.posixPermissions: 0o700])
        self.directory = directory
        self.nonce = nonce
        self.shell = shell
        var env = original
        let home = env["HOME"] ?? FileManager.default.homeDirectoryForCurrentUser.path
        guard home.hasPrefix("/") else {
            try? FileManager.default.removeItem(at: directory)
            throw CocoaError(.fileReadInvalidFileName)
        }
        switch shell {
        case .zsh:
            guard (env["ZDOTDIR"] ?? home).hasPrefix("/") else {
                try? FileManager.default.removeItem(at: directory)
                throw CocoaError(.fileReadInvalidFileName)
            }
            env["OBERTH_ORIGINAL_ZDOTDIR"] = env["ZDOTDIR"] ?? home
            env["ZDOTDIR"] = directory.path
            launchArguments = ["-i"]
            try Self.writeZsh(directory: directory, nonce: nonce)
        case .bash:
            let rc = directory.appendingPathComponent("rc")
            env["OBERTH_BASH_HOME"] = home
            // macOS /bin/bash 3.2 requires --rcfile before -i.
            launchArguments = ["--rcfile", rc.path, "-i"]
            try Self.writeBash(rc: rc, nonce: nonce)
        case .fish:
            let configRoot = directory.appendingPathComponent("fish", isDirectory: true)
            try FileManager.default.createDirectory(at: configRoot, withIntermediateDirectories: true)
            let userXDG = env["XDG_CONFIG_HOME"] ?? (home + "/.config")
            guard userXDG.hasPrefix("/") else {
                try? FileManager.default.removeItem(at: directory)
                throw CocoaError(.fileReadInvalidFileName)
            }
            env["OBERTH_FISH_USER_XDG"] = userXDG
            env["XDG_CONFIG_HOME"] = directory.path
            launchArguments = ["-i"]
            try Self.writeFish(config: configRoot.appendingPathComponent("config.fish"), nonce: nonce)
        }
        self.environment = env
    }

    deinit { try? FileManager.default.removeItem(at: directory) }

    private static func writeFile(_ path: URL, _ text: String) throws {
        let body = text.split(separator: "\n", omittingEmptySubsequences: false)
            .map { String($0.drop(while: { $0 == " " })) }
            .joined(separator: "\n")
        guard FileManager.default.createFile(atPath: path.path, contents: Data((body + "\n").utf8), attributes: [.posixPermissions: 0o600]) else {
            throw CocoaError(.fileWriteUnknown)
        }
    }

    private static func writeZsh(directory: URL, nonce: String) throws {
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
        precmd_functions=(__oberth_precmd ${precmd_functions:#__oberth_precmd})
        """#.replacingOccurrences(of: "__NONCE__", with: nonce)
        do {
            try writeFile(directory.appendingPathComponent(".zshenv"), zshenv)
            try writeFile(directory.appendingPathComponent(".zshrc"), zshrc)
        } catch {
            try? FileManager.default.removeItem(at: directory)
            throw error
        }
    }

    private static func writeBash(rc: URL, nonce: String) throws {
        let text = #"""
        [ -r "$OBERTH_BASH_HOME/.bash_profile" ] && . "$OBERTH_BASH_HOME/.bash_profile"
        [ -r "$OBERTH_BASH_HOME/.bashrc" ] && . "$OBERTH_BASH_HOME/.bashrc"
        unset OBERTH_BASH_HOME
        __oberth_running=0
        __oberth_last_cmd=""
        __oberth_encode() {
          local command="$1"
          command=${command//\\/\\\\}
          command=${command//;/\\x3b}
          command=${command//$'\n'/\\x0a}
          command=${command//$'\r'/\\x0d}
          command=${command//$'\e'/\\x1b}
          command=${command//$'\a'/\\x07}
          printf $'\\033]633;E;%s;__NONCE__\\a' "$command"
        }
        __oberth_prompt() {
          local result=$?
          local entry hist cmd
          entry="$(history 1 2>/dev/null || true)"
          hist="${entry#[[:space:]]*[0-9]*[[:space:]]}"
          cmd="${hist#[[:space:]]}"
          if [ -n "$cmd" ] && [ "$cmd" != "$__oberth_last_cmd" ] && [ ${#cmd} -le 8192 ]; then
            __oberth_last_cmd="$cmd"
            __oberth_encode "$cmd"
            printf $'\\033]133;C;__NONCE__\\a'
            printf $'\\033]133;D;%d;__NONCE__\\a' "$result"
            __oberth_running=0
          elif [ $__oberth_running -eq 1 ]; then
            printf $'\\033]133;D;%d;__NONCE__\\a' "$result"
            __oberth_running=0
          fi
          printf $'\\033]133;A;__NONCE__\\a'
          return $result
        }
        case "$PROMPT_COMMAND" in *__oberth_prompt*) ;; *) PROMPT_COMMAND="__oberth_prompt${PROMPT_COMMAND:+;}$PROMPT_COMMAND" ;; esac
        """#.replacingOccurrences(of: "__NONCE__", with: nonce)
        do {
            try writeFile(rc, text)
        } catch {
            try? FileManager.default.removeItem(at: rc.deletingLastPathComponent())
            throw error
        }
    }

    private static func writeFish(config: URL, nonce: String) throws {
        let text = #"""
        set -q OBERTH_FISH_USER_XDG
        and set -l user_config "$OBERTH_FISH_USER_XDG/fish/config.fish"
        and test -f $user_config
        and source $user_config
        set -e OBERTH_FISH_USER_XDG
        set -g __oberth_running 0
        function __oberth_fish_encode -a command
          set command (string replace -a -- '\\' '\\\\' -- $command)
          set command (string replace -a -- ';' '\\x3b' -- $command)
          set command (string replace -a -- \n '\\x0a' -- $command)
          set command (string replace -a -- \r '\\x0d' -- $command)
          set command (string replace -a -- \e '\\x1b' -- $command)
          set command (string replace -a -- \a '\\x07' -- $command)
          printf \e]633;E;%s;__NONCE__\a $command
        end
        function __oberth_fish_preexec --on-event fish_preexec
          set -l command (string join ' ' -- $argv)
          if test (string length -- $command) -le 8192
            __oberth_fish_encode $command
          end
          printf \e]133;C;__NONCE__\a
          set -g __oberth_running 1
        end
        function __oberth_fish_postexec --on-event fish_postexec
          set -l result $status
          if test $__oberth_running -eq 1
            printf \e]133;D;%d;__NONCE__\a $result
          end
          set -g __oberth_running 0
          printf \e]133;A;__NONCE__\a
        end
        """#.replacingOccurrences(of: "__NONCE__", with: nonce)
        do {
            try writeFile(config, text)
        } catch {
            try? FileManager.default.removeItem(at: config.deletingLastPathComponent().deletingLastPathComponent())
            throw error
        }
    }
}
