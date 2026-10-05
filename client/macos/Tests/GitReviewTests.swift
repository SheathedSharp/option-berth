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
            ["root":"/fixture/repo"]
        ]
        for json in invalid {
            do { _ = try decode(GitTree.self, json); fatalError("invalid Git facts accepted") }
            catch { checks += 1 }
        }
        // A large in-memory list is filtered, not rendered eagerly or re-read from Git.
        let many = try (0..<10_000).map { try file("file-\($0).swift", " M") }
        expect(GitReviewFilter.all.files(many, query: "file-9999").count == 1, "large-list filtering")
        print("GitReviewTests: \(checks) checks passed")
    }
}
