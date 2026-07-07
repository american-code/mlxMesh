import Foundation

/// Detects whether Exo is installed — NEVER whether it's healthy (that's
/// ExoHealthMonitor's job; presence of the app bundle is not the same as its
/// API server being up, per Exo's own documented history of packaged-app
/// startup bugs).
enum ExoDetector {
    /// Fast-path guesses for the two most common DMG-drag install locations.
    /// Checked first since they're free (no process spawn).
    static let candidateAppPaths = [
        "/Applications/EXO.app",
        (NSHomeDirectory() as NSString).appendingPathComponent("Applications/EXO.app"),
    ]

    /// Returns the actual install path, or nil if Exo genuinely can't be
    /// found anywhere. Fixed-path guessing alone proved unreliable in
    /// practice (a real install was missed by both candidates above), so
    /// this falls back to Spotlight (`mdfind`), which finds the app
    /// regardless of exactly where it was dragged/installed — the robust
    /// answer to "is EXO.app anywhere on this Mac," not a guess.
    static func installedAppPath() -> String? {
        if let fast = candidateAppPaths.first(where: { FileManager.default.fileExists(atPath: $0) }) {
            return fast
        }
        return spotlightFindAppPath()
    }

    private static func spotlightFindAppPath() -> String? {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/mdfind")
        process.arguments = ["kMDItemFSName == 'EXO.app'"]
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = Pipe()
        do {
            try process.run()
            process.waitUntilExit()
        } catch {
            return nil
        }
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        guard let output = String(data: data, encoding: .utf8) else { return nil }
        return output.split(separator: "\n").map(String.init).first { !$0.isEmpty }
    }

    static func isAppInstalled() -> Bool {
        installedAppPath() != nil
    }

    /// Known fixed Homebrew binary locations (Apple Silicon vs Intel).
    /// Deliberately NOT resolved via `/usr/bin/env` + PATH search: a
    /// GUI-launched app (Xcode Run, or a double-clicked .app) gets a minimal
    /// PATH that does NOT include /opt/homebrew/bin — confirmed empirically
    /// (`env -i PATH="/usr/bin:/bin:/usr/sbin:/sbin" env brew --version`
    /// fails with "No such file or directory" even when brew is genuinely
    /// installed). Checking these fixed locations directly is what every
    /// GUI app that shells out to Homebrew has to do for exactly this
    /// reason — PATH-based lookup silently reports "not installed" for a
    /// real, working Homebrew install.
    static func brewExecutableURL() -> URL? {
        for path in ["/opt/homebrew/bin/brew", "/usr/local/bin/brew"] {
            if FileManager.default.isExecutableFile(atPath: path) {
                return URL(fileURLWithPath: path)
            }
        }
        return nil
    }

    /// Secondary signal: installed via `brew install --cask exo`. Either this
    /// OR the app-bundle check counts as "installed" — a user might have one
    /// without the other (DMG install without brew, or a cask install whose
    /// app got moved/renamed).
    static func isHomebrewCaskInstalled() -> Bool {
        guard let brew = brewExecutableURL() else { return false } // brew itself not found — not an error, just "no signal"
        let process = Process()
        process.executableURL = brew
        process.arguments = ["list", "--cask", "exo"]
        process.standardOutput = Pipe()
        process.standardError = Pipe()
        do {
            try process.run()
            process.waitUntilExit()
            return process.terminationStatus == 0
        } catch {
            return false
        }
    }

    static func isInstalled() -> Bool {
        isAppInstalled() || isHomebrewCaskInstalled()
    }
}
