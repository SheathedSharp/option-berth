import Foundation

public struct CommandBlock: Identifiable, Equatable {
    public let id: UUID
    public let command: String?
    public let startedAt: Date
    public var endedAt: Date?
    public var exitCode: Int?
    public var interrupted: Bool
    public var isRunning: Bool { endedAt == nil }
}

/// Incremental, bounded OSC parser for the opt-in local shell hooks. The nonce
/// prevents accidental remote-shell/prompt markers from being attributed locally;
/// it is not a security boundary against programs in the same shell environment.
/// Markers are display metadata only, never service facts or execution authority.
public struct CommandBlockParser {
    public private(set) var blocks: [CommandBlock] = []
    public private(set) var revision: UInt64 = 0
    private let nonce: String
    private var mode = 0 // ground / ESC / OSC / OSC-ESC / discard / discard-ESC
    private var frame: [UInt8] = []
    private var pendingCommand: String?
    public static let maximumBlocks = 256
    public static let maximumFrameBytes = 16 * 1024
    public init(nonce: String) { self.nonce = nonce }

    public mutating func consume(_ bytes: ArraySlice<UInt8>, now: Date = Date()) {
        for byte in bytes {
            switch mode {
            case 0: if byte == 27 { mode = 1 }
            case 1:
                if byte == 93 { frame.removeAll(keepingCapacity: true); mode = 2 }
                else { mode = byte == 27 ? 1 : 0 }
            case 2:
                if byte == 7 { finishFrame(now); mode = 0 }
                else if byte == 27 { mode = 3 }
                else { append(byte) }
            case 3:
                if byte == 92 { finishFrame(now); mode = 0 }
                else { frame.removeAll(keepingCapacity: true); mode = byte == 27 ? 1 : 0 }
            case 4:
                if byte == 7 { mode = 0 }
                else if byte == 27 { mode = 5 }
            default:
                mode = byte == 92 || byte == 7 ? 0 : (byte == 27 ? 5 : 4)
            }
        }
    }
    private mutating func append(_ byte: UInt8) {
        if frame.count >= Self.maximumFrameBytes { frame.removeAll(keepingCapacity: false); pendingCommand = nil; mode = 4 }
        else { frame.append(byte) }
    }
    private mutating func finishFrame(_ now: Date) {
        defer { frame.removeAll(keepingCapacity: true) }
        guard !nonce.isEmpty, let text = String(bytes: frame, encoding: .utf8) else { return }
        let fields = text.split(separator: ";", omittingEmptySubsequences: false).map(String.init)
        if fields.count == 4, fields[0] == "633", fields[1] == "E", fields[3] == nonce {
            pendingCommand = Self.decodeCommand(fields[2]); return
        }
        guard fields.first == "133", fields.last == nonce else { return }
        if fields.count == 3, fields[1] == "C" {
            interrupt(now: now)
            revision &+= 1
            blocks.append(CommandBlock(id: UUID(), command: pendingCommand, startedAt: now, interrupted: false))
            pendingCommand = nil
            if blocks.count > Self.maximumBlocks { blocks.removeFirst(blocks.count - Self.maximumBlocks) }
        } else if fields.count == 4, fields[1] == "D", let last = blocks.indices.last, blocks[last].isRunning {
            revision &+= 1
            blocks[last].endedAt = now
            if let code = Int(fields[2]), (0...255).contains(code), String(code) == fields[2] {
                blocks[last].exitCode = code
            } else { blocks[last].interrupted = true }
        } else if fields.count == 3, fields[1] == "A" {
            // A new prompt with no D marker cannot invent a successful exit.
            interrupt(now: now); pendingCommand = nil
        }
    }
    public mutating func interrupt(now: Date = Date()) {
        if let last = blocks.indices.last, blocks[last].isRunning {
            revision &+= 1
            blocks[last].endedAt = now; blocks[last].interrupted = true
        }
    }
    public mutating func clear() { revision &+= 1; blocks.removeAll(); pendingCommand = nil }
    private static func decodeCommand(_ value: String) -> String? {
        guard value.utf8.count <= 12 * 1024 else { return nil }
        var result = [UInt8](), bytes = Array(value.utf8), i = 0
        while i < bytes.count {
            if bytes[i] == 92 {
                if i + 3 < bytes.count, bytes[i + 1] == 120,
                   let n = UInt8(String(bytes: bytes[(i + 2)...(i + 3)], encoding: .ascii) ?? "", radix: 16) {
                    result.append(n); i += 4
                } else if i + 1 < bytes.count, bytes[i + 1] == 92 { result.append(92); i += 2 }
                else { return nil }
            } else { result.append(bytes[i]); i += 1 }
        }
        guard result.count <= 8192, !result.contains(0), !result.contains(27), !result.contains(7) else { return nil }
        return String(bytes: result, encoding: .utf8)
    }
}
