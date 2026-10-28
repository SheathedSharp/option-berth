import Foundation
import BerthTerminal
import BerthAgent
@testable import BerthClient

extension ClientChecks {
    /// The console keeps no workplace record of its own: it reports what it
    /// owns through `oberth workplace record` and projects `oberth workplace
    /// show`. These checks drive that whole path against a synthetic CLI, so
    /// they exercise the real argument marshalling and JSON decoding without a
    /// daemon.
    @MainActor
    static func workplaceChecks() async throws {
        let fm = FileManager.default
        let root = fm.temporaryDirectory.appendingPathComponent("workplace-check-" + UUID().uuidString, isDirectory: true)
        let worktree = root.appendingPathComponent("tree", isDirectory: true)
        try fm.createDirectory(at: worktree, withIntermediateDirectories: true)
        defer { try? fm.removeItem(at: root) }

        let state = root.appendingPathComponent("workplaces.json").path
        let binary = try writeSyntheticOberth(into: root, state: state)
        let store = WorkplaceStore(binary: binary)

        let first = try await store.ensureActive(worktree: worktree.path)
        require(first.name == "默认", "default workplace name")
        require(store.list(worktree: worktree.path).count == 1, "one workplace after ensure")

        let shell = CommandBlock(
            id: UUID(), command: "ls", startedAt: Date(timeIntervalSince1970: 1_700_000_000),
            endedAt: Date(timeIntervalSince1970: 1_700_000_001), exitCode: 0, interrupted: false,
            capturedOutput: "a\nb")
        let agent = AgentRow(
            id: UUID(), sessionID: UUID(), provider: "opencode", transport: .terminal,
            title: "OpenCode 1", status: .completed, activity: nil,
            since: Date(timeIntervalSince1970: 1_700_000_002), unread: false, isTarget: false,
            queuedCount: 0, userPrompt: "hi", resultText: "hello", exitCode: nil)
        store.syncLive(shell: [shell], agents: [agent])
        // Quitting must not drop the last cards: the flush reports synchronously.
        store.flushPendingSave()

        // Read back through the engine, not through anything the client kept.
        let reported = try WorkplaceAPI.show(worktree: worktree.path, binary: binary)
        require(reported.blocks.count == 2, "engine holds both reported blocks")
        let shellBlock = reported.blocks.first { $0.id == shell.id.uuidString.lowercased() }
        require(shellBlock?.source == "reported", "console work is reported, not claimed as engine evidence")
        require(shellBlock?.input == "ls", "shell command reached the engine")
        require(shellBlock?.output == "a\nb", "shell output travelled through --output-file")
        require(shellBlock?.status == "completed", "shell status mapped")
        let agentBlock = reported.blocks.first { $0.kind == "agent" }
        require(agentBlock?.provider == "opencode", "agent provider reported")
        require(agentBlock?.status == "completed", "agent status mapped")

        // The same id reported again is an update, not a second block.
        let finished = CommandBlock(
            id: shell.id, command: "ls", startedAt: shell.startedAt, endedAt: shell.endedAt,
            exitCode: 1, interrupted: false, capturedOutput: "boom")
        store.syncLive(shell: [finished], agents: [])
        store.flushPendingSave()
        let updated = try WorkplaceAPI.show(worktree: worktree.path, binary: binary)
        require(updated.blocks.count == 2, "re-reporting the same id updates one block")
        require(updated.blocks.first { $0.id == shell.id.uuidString.lowercased() }?.status == "failed",
                "a failing exit is reported as failed")

        // The console projects the engine's blocks once it refreshes.
        let second = try await store.create(worktree: worktree.path, name: "探索", makeActive: true)
        require(second.name == "探索", "named workplace")
        require(store.blocks.isEmpty, "new workplace starts empty")
        require(store.list(worktree: worktree.path).count == 2, "two workplaces")

        let recalled = try await store.switchTo(worktree: worktree.path, query: "默认")
        require(recalled.engineID == first.engineID, "switch by name")
        require(store.blocks.count == 2, "recalled blocks")
        require(store.historicalShellBlocks(excludingLive: []).count == 1, "historical shell")
        require(store.historicalAgentRows(excludingLive: []).count == 1, "historical agent")

        try await store.delete(worktree: worktree.path, query: "探索")
        require(store.list(worktree: worktree.path).count == 1, "deleted workplace")
        require(store.active?.engineID == first.engineID, "still on default")

        print("PASS: workplace read/report/switch/delete through the engine")
    }

    /// A stand-in for `oberth` that implements only the `workplace` verbs the
    /// console uses, keeping its state in one JSON file.
    private static func writeSyntheticOberth(into root: URL, state: String) throws -> String {
        let url = root.appendingPathComponent("oberth-fake")
        let script = #"""
        #!/usr/bin/env python3
        import datetime, json, os, secrets, sys

        STATE = "\#(state)"

        def load():
            if not os.path.exists(STATE):
                return {"active": None, "items": [], "blocks": {}}
            with open(STATE) as handle:
                return json.load(handle)

        def save(value):
            with open(STATE, "w") as handle:
                json.dump(value, handle)

        def now():
            return datetime.datetime.now(datetime.timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")

        def record(value, item):
            return {"id": item["id"], "name": item["name"], "worktree": item["worktree"],
                    "active": value["active"] == item["id"],
                    "blocks": len(value["blocks"].get(item["id"], [])),
                    "created_at": item["created_at"], "updated_at": item["updated_at"]}

        def current(value):
            return next((item for item in value["items"] if item["id"] == value["active"]), None)

        def main():
            argv = sys.argv[1:]
            if not argv or argv[0] != "workplace":
                sys.stderr.write("fake oberth: only workplace is implemented\n")
                return 2
            command = argv[1] if len(argv) > 1 else ""
            flags, positional, index = {}, [], 2
            while index < len(argv):
                token = argv[index]
                if token == "--json":
                    flags["json"] = True
                    index += 1
                elif token.startswith("--"):
                    flags[token[2:]] = argv[index + 1] if index + 1 < len(argv) else ""
                    index += 2
                else:
                    positional.append(token)
                    index += 1
            value = load()
            worktree = flags.get("worktree", "")
            if command == "list":
                print(json.dumps({"worktree": worktree,
                                  "workplaces": [record(value, item) for item in value["items"]]}))
            elif command == "new":
                name = positional[0] if positional else "默认"
                item = {"id": secrets.token_hex(8), "name": name, "worktree": worktree,
                        "created_at": now(), "updated_at": now()}
                value["items"].append(item)
                value["active"] = item["id"]
                save(value)
                print(json.dumps({"workplace": record(value, item)}))
            elif command == "use":
                query = positional[0]
                match = next((item for item in value["items"]
                              if item["id"] == query or item["name"] == query), None)
                if match is None:
                    sys.stderr.write("no such workplace\n")
                    return 1
                value["active"] = match["id"]
                save(value)
                print(json.dumps({"workplace": record(value, match)}))
            elif command == "show":
                item = current(value)
                if item is None:
                    sys.stderr.write("no active workplace\n")
                    return 1
                print(json.dumps({"workplace": record(value, item),
                                  "blocks": value["blocks"].get(item["id"], []),
                                  "truncated": False}))
            elif command == "delete":
                query = positional[0]
                value["items"] = [item for item in value["items"]
                                  if not (item["id"] == query or item["name"] == query)]
                value["blocks"].pop(query, None)
                if value["active"] == query:
                    value["active"] = value["items"][-1]["id"] if value["items"] else None
                save(value)
                print(json.dumps({"workplace": None}))
            elif command == "record":
                item = current(value)
                if item is None:
                    sys.stderr.write("no active workplace\n")
                    return 1
                blocks = value["blocks"].setdefault(item["id"], [])
                block_id = flags.get("id", "")
                output = flags.get("output")
                if "output-file" in flags:
                    with open(flags["output-file"]) as handle:
                        output = handle.read()
                block = next((entry for entry in blocks if entry["id"] == block_id), None)
                if block is None:
                    block = {"id": block_id, "workplace": item["id"], "seq": len(blocks) + 1}
                    blocks.append(block)
                block.update({"kind": flags.get("kind", "shell"), "source": "reported",
                              "status": flags.get("status", "running"),
                              "started_at": flags.get("started-at", now()),
                              "ended_at": flags.get("ended-at", now())})
                if "input" in flags:
                    block["input"] = flags["input"]
                if output is not None:
                    block["output"] = output
                if "exit-code" in flags:
                    block["exit_code"] = int(flags["exit-code"])
                if "provider" in flags:
                    block["provider"] = flags["provider"]
                item["updated_at"] = now()
                save(value)
                print(json.dumps({"workplace": record(value, item), "block": block}))
            else:
                sys.stderr.write("fake oberth: unknown command " + command + "\n")
                return 2
            return 0

        sys.exit(main())
        """#
        try script.write(to: url, atomically: true, encoding: .utf8)
        try fmSetExecutable(url)
        return url.path
    }

    private static func fmSetExecutable(_ url: URL) throws {
        try FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)
    }
}
