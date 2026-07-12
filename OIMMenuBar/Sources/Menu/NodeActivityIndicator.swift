import SwiftUI

extension AppState {
    /// What the node is doing right now, distinct from `IconState` (which is
    /// about health/attention) — this is about work: is it still coming up,
    /// serving a real paying request this instant, or just sitting connected
    /// and collecting the small periodic idle-availability reward. Drives
    /// NodeActivityIndicator's animation so a contributor gets a glanceable,
    /// continuously-live signal that their Mac is actually doing something,
    /// not just a static "Running" label that looks identical whether it's
    /// earning real money or has been silently stuck for hours.
    enum NodeActivity: Hashable {
        case connecting
        case earningInference
        case earningIdle
        case notRunning
    }

    var nodeActivity: NodeActivity {
        switch nodeController.state {
        case .stopped, .failed:
            return .notRunning
        case .starting:
            return .connecting
        case .running:
            // /detect hasn't reported back yet — still coming up even though
            // the process itself is technically "running".
            guard detectMonitor.snapshot != nil else { return .connecting }
            if let inFlight = podMetricsMonitor.inFlightJobs, inFlight > 0 {
                return .earningInference
            }
            return .earningIdle
        }
    }
}

/// Live activity readout for the popover. Three states with genuinely
/// different animations, not just three colors on the same dot:
///   - connecting: a quick pulsing dot — coming up, nothing to measure yet.
///   - earningInference: an indeterminate sweeping bar — real work is running
///     right now but the node has no visibility into its own token timing,
///     so this deliberately does NOT fake a token-by-token progress readout.
///   - earningIdle: a real mm:ss countdown + fill bar cycling on the
///     coordinator's documented default availability-reward cadence.
struct NodeActivityIndicator: View {
    let activity: AppState.NodeActivity

    var body: some View {
        switch activity {
        case .notRunning:
            EmptyView()
        case .connecting:
            HStack(spacing: 5) {
                PulsingDot(color: .yellow, period: 0.7)
                Text("Connecting…")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
            }
        case .earningInference:
            VStack(alignment: .leading, spacing: 3) {
                Text("Earning — serving inference")
                    .font(.caption2)
                    .foregroundStyle(.secondary)
                SweepingBar(color: .green)
            }
        case .earningIdle:
            IdleRewardCountdown()
        }
    }
}

/// A small circle that scales up and fades out on a repeating loop.
private struct PulsingDot: View {
    let color: Color
    let period: Double
    @State private var pulsed = false

    var body: some View {
        Circle()
            .fill(color)
            .frame(width: 6, height: 6)
            .scaleEffect(pulsed ? 1.8 : 1.0)
            .opacity(pulsed ? 0.25 : 1.0)
            .animation(.easeInOut(duration: period).repeatForever(autoreverses: true), value: pulsed)
            .onAppear { pulsed = true }
    }
}

/// An indeterminate progress indicator (a short filled segment sweeping back
/// and forth) for work of unknown duration — used while a real job is in
/// flight, since the node has no per-token timing to report honestly.
private struct SweepingBar: View {
    let color: Color
    @State private var atEnd = false

    var body: some View {
        GeometryReader { geo in
            Capsule()
                .fill(color.opacity(0.2))
                .overlay(alignment: .leading) {
                    Capsule()
                        .fill(color)
                        .frame(width: geo.size.width * 0.32)
                        .offset(x: atEnd ? geo.size.width * 0.68 : 0)
                }
                .clipShape(Capsule())
        }
        .frame(height: 4)
        .onAppear {
            withAnimation(.easeInOut(duration: 1.0).repeatForever(autoreverses: true)) {
                atEnd = true
            }
        }
    }
}

/// A live mm:ss countdown + fill bar cycling on the coordinator's documented
/// default availability-reward interval (~10 min, jittered — see RUNBOOK.md,
/// --availability-reward-interval). This is a client-side ESTIMATE, not a
/// value read from the server: the coordinator picks probe targets and
/// timing entirely on its own side and never tells a node when its next
/// check-in will land (and the interval is randomly jittered besides), so a
/// bar claiming to know the exact moment credit arrives would be dishonest.
/// What this gives instead is a truthful, satisfying "something is actively
/// cycling toward your next credit" signal, restarting the cycle whenever a
/// job is actually observed in flight (the moment the real idle clock resets
/// server-side too).
private struct IdleRewardCountdown: View {
    static let cycleSeconds: TimeInterval = 10 * 60

    @State private var elapsed: TimeInterval = 0
    private let timer = Timer.publish(every: 1, on: .main, in: .common).autoconnect()

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            Text("Connected — earning idle credits · next check-in ~\(remainingLabel)")
                .font(.caption2)
                .foregroundStyle(.secondary)
            ProgressView(value: elapsed, total: Self.cycleSeconds)
                .tint(.blue)
        }
        .onReceive(timer) { _ in
            elapsed = elapsed >= Self.cycleSeconds ? 0 : elapsed + 1
        }
    }

    private var remainingLabel: String {
        let remaining = max(0, Self.cycleSeconds - elapsed)
        let m = Int(remaining) / 60
        let s = Int(remaining) % 60
        return String(format: "%d:%02d", m, s)
    }
}
