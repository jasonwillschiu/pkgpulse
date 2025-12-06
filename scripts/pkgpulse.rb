# Homebrew formula for pkgpulse
# This file is a template - copy to your homebrew-tap repository
#
# Setup steps:
# 1. Create a GitHub repo: github.com/<username>/homebrew-tap
# 2. Copy this file to: homebrew-tap/Formula/pkgpulse.rb
# 3. Update the url and sha256 for each release
#
# Users install with:
#   brew tap <username>/tap
#   brew install pkgpulse

class Pkgpulse < Formula
  desc "CLI tool for analyzing and comparing container image sizes"
  homepage "https://github.com/jasonwillschiu/pkgpulse"
  license "MIT"
  version "0.6.0"

  on_macos do
    on_intel do
      url "https://github.com/jasonwillschiu/pkgpulse/releases/download/v0.6.0/pkgpulse_darwin_amd64.tar.gz"
      sha256 "REPLACE_WITH_ACTUAL_SHA256_FOR_DARWIN_AMD64"
    end

    on_arm do
      url "https://github.com/jasonwillschiu/pkgpulse/releases/download/v0.6.0/pkgpulse_darwin_arm64.tar.gz"
      sha256 "REPLACE_WITH_ACTUAL_SHA256_FOR_DARWIN_ARM64"
    end
  end

  on_linux do
    on_intel do
      url "https://github.com/jasonwillschiu/pkgpulse/releases/download/v0.6.0/pkgpulse_linux_amd64.tar.gz"
      sha256 "REPLACE_WITH_ACTUAL_SHA256_FOR_LINUX_AMD64"
    end

    on_arm do
      url "https://github.com/jasonwillschiu/pkgpulse/releases/download/v0.6.0/pkgpulse_linux_arm64.tar.gz"
      sha256 "REPLACE_WITH_ACTUAL_SHA256_FOR_LINUX_ARM64"
    end
  end

  depends_on "syft"

  def install
    bin.install "pkgpulse"
  end

  test do
    assert_match version.to_s, shell_output("#{bin}/pkgpulse --version")
  end
end
