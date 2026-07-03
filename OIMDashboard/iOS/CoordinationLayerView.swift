import SwiftUI

/// iOS counterpart of the web dashboard's Security-layer panel: iOS coordination
/// participants shown with a distinct shield/device icon, plus a show/hide
/// toggle for the whole layer. Additive — the mesh routes normally without them.
struct CoordinationLayerView: View {
    @Environment(TopologyStore.self) private var store
    @State private var show = true

    private var participants: [CoordinationParticipant] { store.allCoordination }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Image(systemName: "lock.shield.fill").foregroundStyle(.purple)
                Text("Security & coordination").font(.headline)
                Text("\(participants.count)")
                    .font(.caption).fontWeight(.bold).foregroundStyle(.purple)
                    .padding(.horizontal, 8).padding(.vertical, 2)
                    .background(.purple.opacity(0.15), in: Capsule())
                Spacer()
                Toggle("", isOn: $show).labelsHidden().tint(.purple)
            }
            Text("iOS devices classifying on-device & hosting encrypted pointers — additive; the mesh routes normally without them.")
                .font(.caption2).foregroundStyle(.secondary)

            if show && !participants.isEmpty {
                ForEach(participants) { p in
                    HStack(spacing: 10) {
                        Image(systemName: p.isMobile ? "ipad" : "lock.shield")
                            .foregroundStyle(.purple)
                        VStack(alignment: .leading, spacing: 1) {
                            Text(String(p.deviceId.prefix(14)) + "…")
                                .font(.system(size: 12, design: .monospaced))
                            Text("\(p.geographicHint.uppercased()) · \(p.role.replacingOccurrences(of: "_", with: " "))")
                                .font(.caption2).foregroundStyle(.secondary)
                        }
                        Spacer()
                        // Served-pointer count — the work this device has done.
                        if let served = p.pointersServed, served > 0 {
                            VStack(alignment: .trailing, spacing: 1) {
                                Text("\(served)")
                                    .font(.system(size: 15, weight: .bold, design: .rounded))
                                    .foregroundStyle(.purple).monospacedDigit()
                                Text("served").font(.caption2).foregroundStyle(.secondary)
                            }
                        }
                    }
                    .padding(.vertical, 4)
                }
            } else if show {
                Text("No iOS coordination devices connected right now.")
                    .font(.caption).foregroundStyle(.secondary)
            }
        }
        .padding()
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 14))
    }
}
