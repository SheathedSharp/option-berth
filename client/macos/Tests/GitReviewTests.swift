import Foundation

@main enum GitReviewTests {
    static func main() throws {
        var checks = 0
        func expect(_ value: @autoclosure () -> Bool, _ message: String) {
            guard value() else { fatalError(message) }; checks += 1
        }
        func decode<T: Decodable>(_ type: T.Type, _ value: Any) throws -> T {
            try JSONDecoder().decode(type, from: JSONSerialization.data(withJSONObject: value))
        }
        func file(_ path: String, _ status: String, _ extra: [String: Any] = [:]) throws -> GitFile {
            try decode(GitFile.self, ["path":path, "status":status].merging(extra) { _, new in new })
        }
        var files: [GitFile] = []
        for status in ["DD", "AU", "UD", "UA", "DU", "AA", "UU"] {
            let f = try file(status, status)
            expect(f.isConflict && !f.isStaged && !f.isUnstaged && f.statusLabel == "冲突", "unmerged classification: " + status)
            files.append(f)
        }
        let both = try file("Sources/API.swift", "MM", ["additions":12, "deletions":3])
        let rename = try file("Sources/New.swift", "R ", ["old_path":"legacy/旧文件.swift", "additions":2, "deletions":0])
        let untracked = try file("notes.md", "??", ["additions":5, "deletions":0])
        let unknown = try file("big.dat", " M")
        let binary = try file("logo.png", " M", ["binary":true])
        files += [both, rename, untracked, unknown, binary]
        expect(both.isStaged && both.isUnstaged && both.statusLabel == "暂存 + 工作区", "MM dual state")
        expect(GitReviewFilter.staged.files(files, query: "").count == 2, "staged count")
        expect(GitReviewFilter.unstaged.files(files, query: "").count == 3, "unstaged count")
        expect(GitReviewFilter.untracked.files(files, query: "").count == 1, "untracked count")
        expect(GitReviewFilter.all.files(files, query: "").count == files.count, "dual-state path duplicated")
        expect(GitReviewFilter.all.files(files, query: "  api.SWIFT ").first?.path == both.path, "case-insensitive path")
        expect(GitReviewFilter.all.files(files, query: "旧文件").first?.path == rename.path, "rename original path search")
        expect(GitReviewFilter.conflicts.files(files, query: "Sources").isEmpty, "combined status/path filter")
        expect(GitReviewFilter.all.files(files, query: "").first?.isConflict == true, "conflicts-first ordering")
        let totals = GitReviewLineTotals(files)
        expect(totals.additions == 19 && totals.deletions == 3 && totals.uncounted == 9, "unknown counts reported as zero")
        let huge = try file("huge", "M ", ["additions":Int.max, "deletions":0])
        let overflow = GitReviewLineTotals([huge, both])
        expect(overflow.additions == Int.max && overflow.uncounted == 1, "overflow trapped or silently wrapped")
        let clean: [String: Any] = ["root":"/fixture/repo", "staged":0, "unstaged":0, "untracked":0, "conflicts":0, "files":NSNull()]
        let cleanTree = try decode(GitTree.self, clean)
        expect(cleanTree.files.isEmpty && cleanTree.overview.clean, "Go nil slice is valid empty array")
        let graphJSON: [String: Any] = [
            "root":"/fixture/repo", "observed_head":"abcdef0123456789", "branch":"feature/review",
            "ahead":2, "behind":1, "truncated":true, "limit":2,
            "commits":[["hash":"abcdef0123456789", "parents":["0123456789abcdef"], "author":"Ada", "when":"2026-10-06T00:00:00Z", "subject":"graph head", "refs":["feature/review"]]],
            "refs":[["name":"feature/review", "target":"abcdef0123456789", "kind":"branch", "current":true]],
            "worktrees":[["path":"/fixture/repo", "branch":"feature/review", "current":true]]
        ]
        let graph = try decode(GitGraph.self, graphJSON)
        expect(graph.branchName == "feature/review" && graph.commits.count == 1 && graph.truncated,
               "graph snake_case decode and bounded facts")
        let unlimited = try decode(GitGraph.self, graphJSON.merging(["limit": 0, "truncated": false]) { _, v in v })
        expect(unlimited.limit == 0 && !unlimited.truncated && unlimited.commits.count == graph.commits.count,
               "limit 0 is the full history")
        expect(graph.commits[0].id == graph.commits[0].hash && graph.refs[0].current,
               "graph IDs and current ref")
        let unknownOverview = try decode(GitOverview.self, ["root":"/fixture/repo"])
        expect(!unknownOverview.clean && !unknownOverview.countsComplete, "missing counters looked clean")
        let invalid: [[String: Any]] = [
            clean.merging(["staged":"oops"]) { _, v in v },
            clean.merging(["staged":-1]) { _, v in v },
            clean.merging(["files":"not an array"]) { _, v in v },
            clean.merging(["files":[["path":"x", "status":true]]]) { _, v in v },
            clean.merging(["files":[["path":"x", "status":"MM"], ["path":"x", "status":"M "]]]) { _, v in v },
            clean.merging(["files":[["path":"x", "status":"M ", "additions":-1]]]) { _, v in v },
            clean.merging(["worktrees":"bad"]) { _, v in v },
            graphJSON.merging(["limit":0]) { _, v in v },
            graphJSON.merging(["commits":[["hash":"abcdef0123456789", "subject":"one"], ["hash":"abcdef0123456789", "subject":"two"]]]) { _, v in v },
            ["root":"/fixture/repo"]
        ]
        for json in invalid {
            do { _ = try decode(GitTree.self, json); fatalError("invalid Git facts accepted") }
            catch { checks += 1 }
        }
        for json in [
            graphJSON.merging(["limit": -1]) { _, v in v },
            graphJSON.merging(["commits":[["hash":"abcdef0123456789", "subject":"one"], ["hash":"abcdef0123456789", "subject":"two"]]]) { _, v in v }
        ] {
            do { _ = try decode(GitGraph.self, json); fatalError("invalid Git graph accepted") }
            catch { checks += 1 }
        }
        // A large in-memory list is filtered, not rendered eagerly or re-read from Git.
        let many = try (0..<10_000).map { try file("file-\($0).swift", " M") }
        expect(GitReviewFilter.all.files(many, query: "file-9999").count == 1, "large-list filtering")
        let rows = GitPatchRows.make([
            GitPatchLine(kind: "meta", text: "diff --git a/a.swift b/a.swift"),
            GitPatchLine(kind: "hunk", text: "@@ -8,3 +8,4 @@ func start()"),
            GitPatchLine(kind: "context", text: " let server = Server()"),
            GitPatchLine(kind: "del", text: "-server.start()"),
            GitPatchLine(kind: "add", text: "+server.start(port: port)"),
            GitPatchLine(kind: "add", text: "+"),
            GitPatchLine(kind: "meta", text: "\\ No newline at end of file"),
            GitPatchLine(kind: "hunk", text: "@@ -0,0 +1 @@"),
            GitPatchLine(kind: "add", text: "+only"),
            GitPatchLine(kind: "hunk", text: "@@ -4 +0,0 @@"),
            GitPatchLine(kind: "del", text: "-gone"),
            GitPatchLine(kind: "hunk", text: "@@ -1, +1 @@"),
            GitPatchLine(kind: "add", text: "+skipped"),
            GitPatchLine(kind: "hunk", text: "@@ -" + String(repeating: "9", count: 40) + " +1 @@"),
            GitPatchLine(kind: "context", text: " overflow")
        ])
        expect(rows.count == 15, "patch projection dropped a contract line")
        expect(rows[0].kind == "meta" && rows[0].oldNumber == nil && rows[0].text.hasPrefix("diff --git"), "file metadata numbered")
        expect(rows[1].kind == "hunk" && rows[1].oldNumber == nil && rows[1].newNumber == nil, "hunk header consumed a line number")
        expect(rows[2] == GitPatchRow(kind: "context", text: "let server = Server()", oldNumber: 8, newNumber: 8), "context numbers")
        expect(rows[3] == GitPatchRow(kind: "del", text: "server.start()", oldNumber: 9, newNumber: nil), "deletion numbers")
        expect(rows[4] == GitPatchRow(kind: "add", text: "server.start(port: port)", oldNumber: nil, newNumber: 9), "addition numbers")
        expect(rows[5] == GitPatchRow(kind: "add", text: "", oldNumber: nil, newNumber: 10), "empty addition")
        expect(rows[6].kind == "meta" && rows[6].oldNumber == nil && rows[6].newNumber == nil, "newline note shifted the cursor")
        expect(rows[8] == GitPatchRow(kind: "add", text: "only", oldNumber: nil, newNumber: 1), "new file invented an old number")
        expect(rows[10] == GitPatchRow(kind: "del", text: "gone", oldNumber: 4, newNumber: nil), "deleted file invented a new number")
        expect(rows[12].newNumber == nil && rows[14].oldNumber == nil, "malformed hunk invented numbers")
        expect(GitPatchRows.digits(rows) == 2, "gutter width")
        let omitted = GitPatchRows.make([
            GitPatchLine(kind: "hunk", text: "@@ -1 +1,2 @@"),
            GitPatchLine(kind: "context", text: " a"),
            GitPatchLine(kind: "add", text: "+b")
        ])
        expect(omitted[1].oldNumber == 1 && omitted[1].newNumber == 1 && omitted[2].newNumber == 2, "omitted hunk count is not 1")
        let chain = try decode(GitGraph.self, [
            "root": "/fixture/repo", "limit": 2,
            "commits": [
                ["hash": "aaaaaaaaaaaaaaaa", "parents": ["bbbbbbbbbbbbbbbb"], "subject": "child"],
                ["hash": "bbbbbbbbbbbbbbbb", "parents": ["cccccccccccccccc"], "subject": "parent"]
            ]
        ])
        let fromContract = GitGraphLayout.layout(chain.commits.map { GitGraphNode(hash: $0.hash, parents: $0.parents) })
        expect(fromContract.rows[0].parentDepartures == chain.commits[0].parents.count
               && fromContract.rows[0].dotColumn == fromContract.rows[1].dotColumn, "declared parent edge")
        expect(fromContract.rows[1].parentDepartures == chain.commits[1].parents.count, "reverse log order became an edge")
        let side = try decode(GitGraph.self, [
            "root": "/fixture/repo", "limit": 2,
            "commits": [
                ["hash": "aaaaaaaaaaaaaaaa", "parents": ["dddddddddddddddd"], "subject": "head"],
                ["hash": "bbbbbbbbbbbbbbbb", "subject": "other"]
            ]
        ])
        let sideLayout = GitGraphLayout.layout(side.commits.map { GitGraphNode(hash: $0.hash, parents: $0.parents) })
        expect(sideLayout.rows[1].parentDepartures == 0 && sideLayout.rows[0].dotColumn != sideLayout.rows[1].dotColumn,
               "adjacent rows invented a parent edge")
        let chainLayout = GitGraphLayout.layout([
            GitGraphNode(hash: "a", parents: ["b"]),
            GitGraphNode(hash: "b", parents: ["c"]),
            GitGraphNode(hash: "c", parents: [])
        ])
        expect(chainLayout.rows.map(\.dotColumn) == [0, 0, 0], "first parent left its lane")
        expect(chainLayout.rows.map(\.dotColor) == [0, 0, 0], "lane color followed the column instead of the branch")
        expect(chainLayout.rows[0].lines == [GitGraphLayout.Line(childIndex: nil, parentIndex: 0, colorIndex: 0)], "chain edge")
        expect(chainLayout.rows[1].lines == [
            GitGraphLayout.Line(childIndex: 0, parentIndex: nil, colorIndex: 0),
            GitGraphLayout.Line(childIndex: nil, parentIndex: 0, colorIndex: 0)
        ], "middle of a chain dropped a parent or child segment")
        expect(chainLayout.columnCount == 1, "straight history opened a second lane")
        let unrelated = GitGraphLayout.layout([
            GitGraphNode(hash: "a", parents: ["d"]),
            GitGraphNode(hash: "b", parents: [])
        ])
        expect(unrelated.rows[0].dotColumn == 0 && unrelated.rows[0].parentDepartures == 1, "declared external parent lost its lane")
        expect(unrelated.rows[1].dotColumn == 1 && unrelated.rows[1].parentDepartures == 0, "adjacent row became a parent")
        let merge = GitGraphLayout.layout([
            GitGraphNode(hash: "m", parents: ["a", "b"]),
            GitGraphNode(hash: "a", parents: ["c"]),
            GitGraphNode(hash: "b", parents: ["c"]),
            GitGraphNode(hash: "c", parents: [])
        ])
        expect(merge.rows.map(\.dotColumn) == [0, 0, 1, 0], "merge lanes")
        expect(merge.rows.map(\.dotColor) == [0, 0, 1, 0], "second parent reused the first lane color")
        expect(merge.rows[0].lines == [
            GitGraphLayout.Line(childIndex: nil, parentIndex: 0, colorIndex: 0),
            GitGraphLayout.Line(childIndex: nil, parentIndex: 1, colorIndex: 1)
        ], "merge did not open a lane per parent")
        expect(merge.rows[2].dotColumn == 1 && merge.rows[2].lines.contains {
            $0.childIndex == nil && $0.parentIndex == 0 && $0.colorIndex == 1
        }, "side branch did not join the first parent")
        let parentCounts = [2, 1, 1, 0]
        expect(zip(merge.rows, parentCounts).allSatisfy { $0.0.parentDepartures == $0.1 }, "a lane was drawn for a parent the commit does not have")
        let diverge = GitGraphLayout.layout([
            GitGraphNode(hash: "a", parents: ["c"]),
            GitGraphNode(hash: "b", parents: ["c"]),
            GitGraphNode(hash: "c", parents: [])
        ])
        expect(diverge.rows.map(\.dotColumn) == [0, 1, 0], "divergent branches shared a column")
        expect(diverge.rows[1].lines.contains { $0.childIndex == nil && $0.parentIndex == diverge.rows[2].dotColumn },
               "divergent branch missed the common ancestor")
        expect(diverge.columnCount == 2, "two heads collapsed to one lane")
        var wide: [GitGraphNode] = []
        wide.reserveCapacity(4_000)
        for index in 0..<4_000 {
            let hash = String(format: "%04d", index)
            let parents: [String]
            if index + 1 >= 4_000 {
                parents = []
            } else if index % 17 == 0 && index + 8 < 4_000 {
                parents = [String(format: "%04d", index + 1), String(format: "%04d", index + 8)]
            } else {
                parents = [String(format: "%04d", index + 1)]
            }
            wide.append(GitGraphNode(hash: hash, parents: parents))
        }
        let started = Date()
        let wideLayout = GitGraphLayout.layout(wide)
        let elapsed = Date().timeIntervalSince(started)
        expect(elapsed < 1, "4000-commit layout took \(elapsed)s")
        expect(wideLayout.rows.count == 4_000 && wideLayout.columnCount == 2, "medium history lane count")
        expect(zip(wideLayout.rows, wide).allSatisfy { $0.0.parentDepartures == $0.1.parents.count }, "medium history invented a parent edge")
        print(String(format: "graph layout 4000 commits: %.1f ms, columns %d", elapsed * 1000, wideLayout.columnCount))
        print("GitReviewTests: \(checks) checks passed")
    }
}
