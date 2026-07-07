import Foundation

/// Mirrors internal/nodeconfig.Config's on-disk shape at
/// ~/.config/oim/config.json — READ-ONLY here. This app expresses its
/// settings as explicit `oim node start` CLI flags on every launch (simpler,
/// one source of truth) rather than writing this file itself; the bridge
/// exists only to prefill the Settings UI with whatever a prior direct CLI
/// run (or the web dashboard's /config endpoint) already saved, so a user
/// who's used the CLI before doesn't see their settings silently reset.
struct NodeConfigBridge: Decodable {
    struct Schedule: Decodable {
        var mode: String?
        var dailyStart: String?
        var dailyEnd: String?
        var days: [String]?

        enum CodingKeys: String, CodingKey {
            case mode
            case dailyStart = "daily_start"
            case dailyEnd = "daily_end"
            case days
        }
    }

    var exoURL: String?
    var memoryCapPct: Double?
    var geographicHint: String?
    var reachabilityEndpoint: String?
    var schedule: Schedule?

    enum CodingKeys: String, CodingKey {
        case exoURL = "exo_url"
        case memoryCapPct = "memory_cap_pct"
        case geographicHint = "geographic_hint"
        case reachabilityEndpoint = "reachability_endpoint"
        case schedule
    }

    static var configPath: URL {
        FileManager.default.homeDirectoryForCurrentUser
            .appendingPathComponent(".config/oim/config.json")
    }

    static func loadExisting() -> NodeConfigBridge? {
        guard let data = try? Data(contentsOf: configPath) else { return nil }
        return try? JSONDecoder().decode(NodeConfigBridge.self, from: data)
    }
}
