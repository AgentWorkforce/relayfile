class Relayfile < Formula
  desc "CLI for RelayFile collaborative file workspaces"
  homepage "https://github.com/agentworkforce/relayfile"
  version "0.0.0"

  on_macos do
    if Hardware::CPU.arm?
      url "https://github.com/agentworkforce/relayfile/releases/download/v#{version}/relayfile_darwin_arm64.tar.gz"
      sha256 "REPLACE_WITH_DARWIN_ARM64_SHA256"
    else
      url "https://github.com/agentworkforce/relayfile/releases/download/v#{version}/relayfile_darwin_amd64.tar.gz"
      sha256 "REPLACE_WITH_DARWIN_AMD64_SHA256"
    end
  end

  on_linux do
    if Hardware::CPU.arm?
      url "https://github.com/agentworkforce/relayfile/releases/download/v#{version}/relayfile_linux_arm64.tar.gz"
      sha256 "REPLACE_WITH_LINUX_ARM64_SHA256"
    else
      url "https://github.com/agentworkforce/relayfile/releases/download/v#{version}/relayfile_linux_amd64.tar.gz"
      sha256 "REPLACE_WITH_LINUX_AMD64_SHA256"
    end
  end

  def install
    bin.install "relayfile"
  end

  test do
    system "#{bin}/relayfile", "help"
  end
end
