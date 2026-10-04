import AppKit
import Foundation
@testable import BerthClient

extension ClientChecks {
    static func updateChecks() throws {
        require(ReleaseVersion("v2.0.0")! > ReleaseVersion("1.99.99")!, "numeric version order failed")
        for invalid in ["01.0.0", "v1.2", "1.2.3-beta", "-1.2.3", "1.2.3.4", "１.2.3", "../v1.2.3", "1.2.3;open", String(repeating: "9", count: 50) + ".0.0"] {
            require(ReleaseVersion(invalid) == nil, "invalid release version accepted")
        }
        func data(_ tag: String, assets: [String] = ["SHA256SUMS", "OptionBerth-v1.2.3-macos-arm64-adhoc.zip"], draft: Bool = false) throws -> Data {
            try JSONSerialization.data(withJSONObject: ["tag_name": tag, "draft": draft, "prerelease": false, "html_url": "https://untrusted.invalid/not-opened", "assets": assets.map { ["name": $0] }])
        }
        let accepted = try data("v1.2.3")
        let offer = try ReleaseOffer.decode(accepted)
        require(offer.hasChecksums && offer.appAssetName?.hasSuffix("-adhoc.zip") == true, "trusted asset name not selected")
        require(offer.page.absoluteString == "https://github.com/SheathedSharp/option-berth/releases/tag/v1.2.3", "release URL accepted an external destination")
        for source in [try data("v1.2.3", draft: true), try data("v1.2.3", assets: ["SHA256SUMS", "SHA256SUMS"]),
                       try data("v1.2.3", assets: ["../../outside"]), try data("v1.2.3", assets: ["bad\u{1b}file"]),
                       Data(repeating: 32, count: 256 * 1024 + 1)] {
            refuses("invalid release metadata accepted") { _ = try ReleaseOffer.decode(source) }
        }
        var prerelease = try JSONSerialization.jsonObject(with: accepted) as! [String: Any]
        prerelease["prerelease"] = true
        refuses("prerelease treated as stable") { _ = try ReleaseOffer.decode(JSONSerialization.data(withJSONObject: prerelease)) }
        let gate = MetadataGate()
        let updates = ReleaseUpdates(loader: { try await gate.next() })
        require(!updates.checking && gate.waiters.isEmpty, "constructing update UI started a request")
        updates.check(); eventually("first lookup not started") { gate.waiters.count == 1 }
        updates.check(); eventually("replacement lookup not started") { gate.waiters.count == 2 }
        gate.finish(0, result: .success(try data("v9.0.0"))); pump()
        require(updates.checking && updates.offer == nil, "obsolete request published or cleared replacement loading state")
        gate.finish(1, result: .success(accepted))
        eventually("current metadata not published") { updates.offer?.version == ReleaseVersion("1.2.3") }
        updates.check(); eventually("cancel fixture not started") { gate.waiters.count == 3 }
        updates.cancel(); gate.finish(2, result: .success(try data("v9.0.0"))); pump()
        require(!updates.checking && updates.offer == nil, "cancelled request republished a result")
        updates.check(); eventually("error fixture not started") { gate.waiters.count == 4 }
        gate.finish(3, result: .failure(NSError(domain: "private-error-canary", code: 1)))
        eventually("lookup error lost") { updates.problem != nil }
        require(updates.problem?.contains("private-error-canary") == false, "untrusted diagnostic leaked")
        require(gate.waiters.allSatisfy { $0 == nil }, "unfinished metadata test task")
        print("PASS: bounded/versioned release metadata, hardcoded release origin, no automatic fetch, stale/cancel/error isolation")
    }
    @MainActor private final class MetadataGate {
        var waiters: [CheckedContinuation<Data, Error>?] = []
        func next() async throws -> Data { try await withCheckedThrowingContinuation { waiters.append($0) } }
        func finish(_ index: Int, result: Result<Data, Error>) { let waiter = waiters[index]; waiters[index] = nil; waiter?.resume(with: result) }
    }
}
