import AppKit
import SwiftUI

/// 应用图标。
///
/// 图标不是一个单独画的图，是**同一个矢量标志**换个尺寸再画一遍 ——
/// 所以 Dock 里的那个形状和左栏里那个形状不可能走开。
///
/// 走 `--write-icon <目录>` 把各档尺寸写成 .iconset，构建脚本再用 `iconutil` 打包。
/// 尺寸和文件名是 .iconset 规定的，不能改。
enum AppIcon {
    @MainActor
    static func write(to directory: String) -> Int32 {
        let specs: [(String, CGFloat)] = [
            ("icon_16x16.png", 16), ("icon_16x16@2x.png", 32),
            ("icon_32x32.png", 32), ("icon_32x32@2x.png", 64),
            ("icon_128x128.png", 128), ("icon_128x128@2x.png", 256),
            ("icon_256x256.png", 256), ("icon_256x256@2x.png", 512),
            ("icon_512x512.png", 512), ("icon_512x512@2x.png", 1024),
        ]
        let dir = URL(fileURLWithPath: directory)
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        } catch {
            FileHandle.standardError.write(Data("建不了目录 \(directory)：\(error.localizedDescription)\n".utf8))
            return 1
        }

        // 同一像素尺寸出现两次（16 与 32 各被引用两回），画一次就够。
        var rendered: [Int: Data] = [:]
        for (name, side) in specs {
            let pixels = Int(side)
            let png: Data
            if let cached = rendered[pixels] {
                png = cached
            } else {
                let renderer = ImageRenderer(content: AppIconView(side: side))
                renderer.scale = 1
                guard let image = renderer.cgImage,
                      let data = NSBitmapImageRep(cgImage: image)
                          .representation(using: .png, properties: [:])
                else {
                    FileHandle.standardError.write(Data("图标 \(name) 没画出来\n".utf8))
                    return 1
                }
                rendered[pixels] = data
                png = data
            }
            do {
                try png.write(to: dir.appendingPathComponent(name))
            } catch {
                FileHandle.standardError.write(Data("写不进 \(name)：\(error.localizedDescription)\n".utf8))
                return 1
            }
        }
        print("已写出 \(dir.path)（\(specs.count) 个文件，\(rendered.count) 档尺寸）")
        return 0
    }
}

/// 图标本体：一块象牙白底 + 标志。
///
/// 底用标志自己的纸色（`Mark.paper` 同一族）—— 标志在界面里是直接落在界面的底上，
/// 在图标里则始终带着自己那块纸，这样 Dock 里那三个颜色不会受桌面壁纸的色温影响。
///
/// 圆角的比例照 macOS 的图标网格来（内容占 824/1024，圆角 185/1024），
/// 这样它和系统里别的图标放在一起是齐的 —— 不然一眼就能看出是外行画的。
struct AppIconView: View {
    let side: CGFloat

    /// 16pt 那一档笔宽加粗一档（`compact`）：真字符 8.86% 的笔宽缩到 16 个像素
    /// 只有 1.2px，会把气口吃掉。这是尺寸上的取舍，不是两套标志。
    private var compact: Bool { side < 32 }

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: side * 0.1807, style: .continuous)
                .fill(
                    LinearGradient(
                        colors: [Color(hex: 0xFDFBF7), Color(hex: 0xF1EADB)],
                        startPoint: .top,
                        endPoint: .bottom
                    )
                )
                .overlay(
                    RoundedRectangle(cornerRadius: side * 0.1807, style: .continuous)
                        .strokeBorder(Color(hex: 0xE2D9C8), lineWidth: max(1, side * 0.0022))
                )
                .padding(side * 0.0977)

            BerthMark(size: side * (compact ? 0.64 : 0.62), compact: compact)
        }
        .frame(width: side, height: side)
    }
}
