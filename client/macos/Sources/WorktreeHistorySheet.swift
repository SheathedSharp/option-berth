import AppKit
import SwiftUI
import BerthTerminal

struct WorktreeHistorySheet: View {
    let root: String
    @ObservedObject var sessions: TerminalSessions
    var frozen = false
    var frozenEntries: [CommandHistoryEntry] = []
    var pasteboard: NSPasteboard = .general
    @Environment(\.dismiss) private var dismiss
    @State private var query = ""
    @State private var confirmingClear = false
    @State private var notice: String?
    @FocusState private var searchFocused: Bool
    private var entries: [CommandHistoryEntry] {
        frozen ? frozenEntries.filter { $0.matches(query) }.sorted(by: CommandHistoryEntry.newestFirst)
               : sessions.commandHistory(in: root, query: query)
    }
    var body: some View {
        let visible = entries
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Label("命令历史 / Command history", systemImage: "clock.arrow.circlepath")
                    .font(Face.sans(14, .semibold))
                Spacer()
                Button("清空此 worktree… / Clear…") { confirmingClear = true }
                    .disabled(frozen || sessions.commandHistory(in: root).isEmpty)
                    .accessibilityIdentifier("history.clear")
                Button("关闭 / Close") { dismiss() }.keyboardShortcut(.cancelAction)
            }
            Text(root).font(Face.mono(10)).foregroundStyle(Ink.inkMuted).lineLimit(1).truncationMode(.middle)
            Text("仅此 worktree 的自有终端 · 事先启用命令块才会记录 · 选择只复制，不执行")
                .font(Face.sans(11)).foregroundStyle(Ink.inkMuted).fixedSize(horizontal: false, vertical: true)
            if frozen {
                Text("搜索命令或会话 / Search commands or sessions").font(Face.sans(12)).foregroundStyle(Ink.inkFaint)
                    .padding(8).frame(maxWidth: .infinity, alignment: .leading).background(Ink.surface)
            } else {
                TextField("搜索命令或会话 / Search commands or sessions", text: $query)
                    .textFieldStyle(.roundedBorder).focused($searchFocused)
                    .accessibilityIdentifier("history.search")
            }
            Text("\(visible.count) 条 / entries").font(Face.mono(10)).foregroundStyle(Ink.inkMuted)
                .accessibilityIdentifier("history.count")
            if frozen { rows(visible).frame(maxHeight: .infinity, alignment: .top) }
            else { ScrollView { rows(visible) } }
            if let notice { Text(notice).font(Face.sans(11)).accessibilityIdentifier("history.notice") }
            Text("内存历史随会话关闭或应用退出丢弃；不导入 shell 历史，不保存到布局快照。\nMemory only. Closing a session or quitting discards its history. No shell-history import or cloud sync.")
                .font(Face.sans(10)).foregroundStyle(Ink.inkMuted).fixedSize(horizontal: false, vertical: true)
        }.padding(18).frame(width: 650, height: 480).background(Ink.canvas).foregroundStyle(Ink.ink)
            .onAppear { if !frozen { searchFocused = true } }
            .alert("清空此 worktree 的命令历史？ / Clear this worktree's history?", isPresented: $confirmingClear) {
                Button("取消 / Cancel", role: .cancel) {}
                Button("清空 / Clear", role: .destructive) {
                    sessions.clearCommandHistory(in: root); notice = "已清空此 worktree / Worktree history cleared"
                }
            } message: {
                Text("仅清除此 worktree 自有终端的内存命令块。不结束进程、不删除其他 worktree 或 Agent 的历史。")
            }
    }
    @ViewBuilder private func rows(_ values: [CommandHistoryEntry]) -> some View {
        if values.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                Text("没有匹配的命令 / No matching commands").font(Face.sans(13, .medium))
                Text("新建 zsh 前显式启用“命令块”；尚未启用的历史不会被补采。")
                    .font(Face.sans(11)).foregroundStyle(Ink.inkMuted)
            }.padding(12).frame(maxWidth: .infinity, alignment: .leading)
        } else {
            LazyVStack(alignment: .leading, spacing: 8) {
                ForEach(values) { entry in
                    HStack(alignment: .top, spacing: 12) {
                        VStack(alignment: .leading, spacing: 6) {
                            HStack {
                                Text(entry.sessionTitle).lineLimit(1)
                                Spacer()
                                Text(entry.status)
                            }.font(Face.sans(10)).foregroundStyle(Ink.inkMuted)
                            Text(entry.block.command ?? "命令文本不可用 / Command text unavailable")
                                .font(Face.mono(11)).lineLimit(3).frame(maxWidth: .infinity, alignment: .leading)
                        }
                        Button("复制 / Copy") {
                            notice = Self.copy(entry, to: pasteboard) ? "已复制；未执行 / Copied, not executed" : "无法复制 / Copy unavailable"
                        }.disabled(entry.block.command == nil || frozen)
                            .accessibilityIdentifier("history.copy." + entry.id)
                    }.padding(10).background(Ink.surface).clipShape(RoundedRectangle(cornerRadius: 6))
                }
            }
        }
    }
    @discardableResult
    static func copy(_ entry: CommandHistoryEntry, to pasteboard: NSPasteboard) -> Bool {
        guard let command = entry.block.command else { return false }
        pasteboard.clearContents()
        return pasteboard.setString(command, forType: .string)
    }
}
