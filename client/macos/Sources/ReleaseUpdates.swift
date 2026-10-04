import AppKit
import Foundation
import SwiftUI

struct ReleaseVersion: Comparable, Equatable {
    let major: Int
    let minor: Int
    let patch: Int
    init?(_ value: String) {
        let text = value.hasPrefix("v") ? String(value.dropFirst()) : value
        let parts = text.split(separator: ".", omittingEmptySubsequences: false)
        guard parts.count == 3, parts.allSatisfy({ !$0.isEmpty && $0.allSatisfy(\.isASCII) && $0.allSatisfy(\.isNumber) && ($0.count == 1 || !$0.hasPrefix("0")) }),
              let a = Int(parts[0]), let b = Int(parts[1]), let c = Int(parts[2]), a >= 0, b >= 0, c >= 0 else { return nil }
        major = a; minor = b; patch = c
    }
    var description: String { "\(major).\(minor).\(patch)" }
    static func < (left: ReleaseVersion, right: ReleaseVersion) -> Bool {
        if left.major != right.major { return left.major < right.major }
        if left.minor != right.minor { return left.minor < right.minor }
        return left.patch < right.patch
    }
}

struct ReleaseOffer: Equatable {
    let version: ReleaseVersion
    let appAssetName: String?
    let hasChecksums: Bool
    var page: URL { URL(string: "https://github.com/SheathedSharp/option-berth/releases/tag/v" + version.description)! }
    static func decode(_ data: Data) throws -> ReleaseOffer {
        struct Document: Decodable {
            struct Asset: Decodable { let name: String }
            let tag_name: String
            let draft: Bool
            let prerelease: Bool
            let assets: [Asset]
        }
        guard data.count <= 256 * 1024 else { throw UpdateFailure.invalid }
        let doc = try JSONDecoder().decode(Document.self, from: data)
        guard !doc.draft, !doc.prerelease, doc.tag_name.hasPrefix("v"), let version = ReleaseVersion(doc.tag_name),
              doc.assets.count <= 64 else { throw UpdateFailure.invalid }
        let names = doc.assets.map(\.name)
        guard Set(names).count == names.count, names.allSatisfy({ $0.utf8.count <= 256 && !$0.contains("/") && !$0.contains("\\") && !$0.unicodeScalars.contains(where: { CharacterSet.controlCharacters.contains($0) }) }) else {
            throw UpdateFailure.invalid
        }
        let candidates = ["OptionBerth-v\(version.description)-macos-arm64-notarized.zip", "OptionBerth-v\(version.description)-macos-arm64-adhoc.zip"]
        return ReleaseOffer(version: version, appAssetName: candidates.first(where: names.contains), hasChecksums: names.contains("SHA256SUMS"))
    }
}

enum UpdateFailure: LocalizedError {
    case invalid, server(Int)
    var errorDescription: String? {
        switch self {
        case .invalid: return "发布元数据无效 / Invalid release metadata"
        case .server(let status): return "无法读取发布信息（HTTP \(status)）/ Release lookup failed"
        }
    }
}

private final class ReleaseRedirectPolicy: NSObject, URLSessionTaskDelegate {
    func urlSession(_ session: URLSession, task: URLSessionTask, willPerformHTTPRedirection response: HTTPURLResponse,
                    newRequest request: URLRequest, completionHandler: @escaping (URLRequest?) -> Void) {
        completionHandler(nil) // Repository redirects must be reviewed, never silently followed.
    }
}

@MainActor
final class ReleaseUpdates: ObservableObject {
    @Published private(set) var offer: ReleaseOffer?
    @Published private(set) var checking = false
    @Published private(set) var problem: String?
    private var request: Task<Void, Never>?
    private var generation = UUID()
    private let loader: () async throws -> Data
    init(loader: @escaping () async throws -> Data = ReleaseUpdates.fetchMetadata) { self.loader = loader }

    func check() {
        request?.cancel()
        let id = UUID(); generation = id
        checking = true; problem = nil; offer = nil
        request = Task { @MainActor in
            defer { if generation == id { checking = false; request = nil } }
            do {
                let value = try ReleaseOffer.decode(await loader())
                try Task.checkCancellation()
                guard generation == id else { return }
                offer = value
            } catch is CancellationError {} catch {
                guard generation == id, !Task.isCancelled else { return }
                problem = (error as? UpdateFailure)?.localizedDescription ?? "更新检查失败；未下载或安装任何内容 / Release check failed"
            }
        }
    }
    func cancel() { generation = UUID(); request?.cancel(); request = nil; checking = false }

    nonisolated private static func fetchMetadata() async throws -> Data {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.httpCookieStorage = nil; configuration.urlCredentialStorage = nil
        configuration.timeoutIntervalForRequest = 15; configuration.timeoutIntervalForResource = 20
        let session = URLSession(configuration: configuration, delegate: ReleaseRedirectPolicy(), delegateQueue: nil)
        defer { session.invalidateAndCancel() }
        let endpoint = URL(string: "https://api.github.com/repos/SheathedSharp/option-berth/releases/latest")!
        var request = URLRequest(url: endpoint)
        request.setValue("application/vnd.github+json", forHTTPHeaderField: "Accept")
        request.setValue("2026-03-10", forHTTPHeaderField: "X-GitHub-Api-Version")
        request.setValue("option-berth-release-check", forHTTPHeaderField: "User-Agent")
        let (bytes, response) = try await session.bytes(for: request)
        guard let http = response as? HTTPURLResponse, http.statusCode == 200, http.url == endpoint else {
            throw UpdateFailure.server((response as? HTTPURLResponse)?.statusCode ?? 0)
        }
        guard response.expectedContentLength <= 256 * 1024 else { throw UpdateFailure.invalid }
        var data = Data()
        for try await byte in bytes {
            try Task.checkCancellation()
            guard data.count < 256 * 1024 else { throw UpdateFailure.invalid }
            data.append(byte)
        }
        return data
    }
}

struct ReleaseUpdateSheet: View {
    var frozen = false
    @StateObject private var updates = ReleaseUpdates()
    @Environment(\.dismiss) private var dismiss
    private var current: String { Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "0.0.0" }
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Label("检查更新 / Check for updates", systemImage: "arrow.down.circle").font(Face.sans(14, .semibold))
                Spacer(); Button("关闭") { dismiss() }.keyboardShortcut(.cancelAction)
            }
            Text("当前版本 \(current)").font(Face.mono(11)).foregroundStyle(Ink.inkMuted)
            if updates.checking { ProgressView("读取官方仓库发布信息…").controlSize(.small) }
            if let offer = updates.offer {
                Text(ReleaseVersion(current).map { $0 < offer.version ? "可用版本 \(offer.version.description)" : "当前版本不低于最新稳定版" } ?? "稳定版本 \(offer.version.description)")
                    .font(Face.sans(13, .semibold))
                if let name = offer.appAssetName { Text(name).font(Face.mono(10)).textSelection(.enabled) }
                else { Text("此发布尚无匹配的 macOS 应用包。").font(Face.sans(11)) }
                Text(offer.hasChecksums ? "发布提供 SHA256SUMS；本机尚未下载或验证安装包。" : "发布未提供汇总校验文件，请勿当作完整安装包。")
                    .font(Face.sans(11)).foregroundStyle(Ink.inkMuted)
                if offer.appAssetName?.contains("-adhoc") == true {
                    Text("此包仅 ad-hoc 签名，不等于 Developer ID/Apple 公证。").font(Face.sans(11)).foregroundStyle(Ink.ink)
                }
                if let currentVersion = ReleaseVersion(current), offer.version.major > currentVersion.major {
                    Text("包含协议步进；升级前先阅读迁移说明并备份状态目录。").font(Face.sans(11))
                }
                Button("查看发布与校验文件") { NSWorkspace.shared.open(offer.page) }
            }
            if let problem = updates.problem { Text(problem).font(Face.sans(11)).foregroundStyle(Ink.ink) }
            Text("只在此窗口检查公开发布元数据，不发送项目路径，不自动下载、安装或重启服务。")
                .font(Face.sans(10)).foregroundStyle(Ink.inkFaint).fixedSize(horizontal: false, vertical: true)
            Button("重新检查") { updates.check() }.disabled(updates.checking)
        }.padding(20).frame(width: 570).background(Ink.canvas).foregroundStyle(Ink.ink)
            .onAppear { if !frozen { updates.check() } }.onDisappear { updates.cancel() }
    }
}
