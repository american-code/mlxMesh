import Foundation

/// Resolves the bundled `oim` binary (embedded at build time into
/// Contents/Resources/oim — see scripts/embed-oim.sh) and reports its
/// stamped version so the UI can show exactly what's running.
enum EmbeddedBinaryLocator {
    static var binaryURL: URL? {
        guard let url = Bundle.main.resourceURL?.appendingPathComponent("oim"),
              FileManager.default.fileExists(atPath: url.path)
        else { return nil }

        // Xcode's Copy Bundle Resources phase does not reliably preserve the
        // Unix executable bit on a plain-file "resource" (a well-known gotcha
        // for embedding CLI binaries this way) — the file can arrive in the
        // built app with the bit stripped even though the source tree copy
        // was chmod +x'd. Self-heal at runtime rather than fail outright:
        // this app is unsandboxed, so chmod'ing its own bundled resource is
        // always permitted.
        if !FileManager.default.isExecutableFile(atPath: url.path) {
            try? FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: url.path)
        }
        return FileManager.default.isExecutableFile(atPath: url.path) ? url : nil
    }

    /// Runs the embedded binary's own `version` subcommand once and returns
    /// its stdout, trimmed. Reading the version this way (rather than baking
    /// a duplicate string into Info.plist) means the displayed version can
    /// never drift from what's actually embedded.
    static func readVersion() async -> String? {
        guard let url = binaryURL else { return nil }
        let process = Process()
        process.executableURL = url
        process.arguments = ["version"]
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = Pipe() // discard

        return await withCheckedContinuation { continuation in
            process.terminationHandler = { _ in
                let data = pipe.fileHandleForReading.readDataToEndOfFile()
                let text = String(data: data, encoding: .utf8)?
                    .trimmingCharacters(in: .whitespacesAndNewlines)
                continuation.resume(returning: text?.isEmpty == false ? text : nil)
            }
            do {
                try process.run()
            } catch {
                continuation.resume(returning: nil)
            }
        }
    }
}
