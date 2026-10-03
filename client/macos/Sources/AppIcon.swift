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

/// 平面象牙白底 + 暖炭双轨 + 陶土运行单元。无渐变、描边或阴影。
/// 固定品牌配色，不让用户的深色主题改变导出的 Dock 图标。
/// 图标比例与 SVG 预览共用 BerthGeometry 的归一化参数。
struct AppIconView: View {
    let side: CGFloat

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: side * CGFloat(BerthGeometry.iconRadius), style: .circular)
                .fill(Mark.paper)
                .padding(side * CGFloat(BerthGeometry.iconInset))

            BerthMark(size: side * CGFloat(BerthGeometry.iconMarkScale), fixedPalette: true)
        }
        .frame(width: side, height: side)
    }
}
