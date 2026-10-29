class OptionBerth < Formula
  desc "Service facts and lifecycle for local Git worktrees"
  homepage "https://github.com/SheathedSharp/option-berth"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/SheathedSharp/option-berth/releases/download/v1.0.0/option-berth-v1.0.0-darwin-arm64.tar.gz"
      sha256 "3803a0153fbda198f49bde0440f4bb46166dcb98721bd486966225ae4c84ede7"
    end
    on_intel do
      url "https://github.com/SheathedSharp/option-berth/releases/download/v1.0.0/option-berth-v1.0.0-darwin-amd64.tar.gz"
      sha256 "d7459e99c33a91427897f90a77dfdd424db5e0cb823914b467cc34814b9e27cd"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/SheathedSharp/option-berth/releases/download/v1.0.0/option-berth-v1.0.0-linux-arm64.tar.gz"
      sha256 "ba0d8c101a9267f44e86a969982acae022f2e22c3cbdd0a9ab77748472c9dfaf"
    end
    on_intel do
      url "https://github.com/SheathedSharp/option-berth/releases/download/v1.0.0/option-berth-v1.0.0-linux-amd64.tar.gz"
      sha256 "880e3a6b3098fc815519dd65d76f292c66843e7fed6eb064576a6d166c09c381"
    end
  end

  def install
    bin.install "oberth", "jev-attention"
    pkgshare.install "LICENSE", "build-manifest.json", "licenses"
  end

  test do
    assert_match "option-berth", shell_output("#{bin}/oberth version")
  end
end
