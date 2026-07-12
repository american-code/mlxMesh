import Foundation

/// Typed errors for the coordinator's error envelope. Every coordinator
/// error is `{"error": "<message>"}`, with a handful of endpoints adding
/// sibling fields (writeErr/writeJSON in cmd/coordinator/main.go — there is
/// no `code`/`type` nesting anywhere in the protocol).
public enum MeshError: Error, LocalizedError {
    /// 402 — the balance check failed before dispatch.
    case insufficientCredits(message: String, balance: Double, required: Double)
    /// 409 — the reservation_id is unknown or its 30s TTL elapsed. Not
    /// recoverable by retrying the same reservation.
    case reservationExpired(message: String)
    /// 429 — per-IP, per-account, or startup-grant-specific rate limit.
    case rateLimited(message: String, retryAfter: TimeInterval?)
    /// 503 — no eligible node available for this job right now.
    case noCapacity(message: String)
    /// Any other non-2xx response.
    case api(message: String, statusCode: Int)

    public var errorDescription: String? {
        switch self {
        case .insufficientCredits(let message, _, _): return message
        case .reservationExpired(let message): return message
        case .rateLimited(let message, _): return message
        case .noCapacity(let message): return message
        case .api(let message, _): return message
        }
    }
}

enum MeshErrorMapper {
    /// Maps a non-2xx HTTPURLResponse + body to the right MeshError case.
    /// No-op (returns nil) for 2xx.
    static func error(for httpResponse: HTTPURLResponse, body: Data) -> MeshError? {
        guard httpResponse.statusCode >= 400 else { return nil }
        let json = (try? JSONSerialization.jsonObject(with: body)) as? [String: Any]
        let message = (json?["error"] as? String) ?? String(data: body, encoding: .utf8) ?? "HTTP \(httpResponse.statusCode)"

        switch httpResponse.statusCode {
        case 402:
            let balance = json?["balance"] as? Double ?? 0
            let required = json?["required"] as? Double ?? 0
            return .insufficientCredits(message: message, balance: balance, required: required)
        case 409:
            return .reservationExpired(message: message)
        case 429:
            let retryAfter = httpResponse.value(forHTTPHeaderField: "Retry-After").flatMap(TimeInterval.init)
            return .rateLimited(message: message, retryAfter: retryAfter)
        case 503:
            return .noCapacity(message: message)
        default:
            return .api(message: message, statusCode: httpResponse.statusCode)
        }
    }
}
