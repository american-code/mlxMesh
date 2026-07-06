import SwiftUI

// TryMeshView is the iOS counterpart of the web dashboard's "Try the mesh": a
// prompt box that submits a real inference job to a coordinator and shows the
// reply plus the throughput/latency measured for THIS request.
struct TryMeshView: View {
    @Environment(TopologyStore.self) private var store

    @State private var prompt = "What can this network do?"
    @State private var sending = false
    @State private var reply: String?
    @State private var stats: (tps: Double?, latency: Int?)?
    @State private var errorMsg: String?

    private var coordinatorURL: String? { store.pods.first?.coordinatorEndpoint }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .firstTextBaseline) {
                Text("Try the mesh").font(.headline)
                Spacer()
                if let served = latestServedLabel {
                    Text(served).font(.caption2).foregroundStyle(.secondary)
                }
            }

            HStack(spacing: 10) {
                TextField("Ask the mesh something…", text: $prompt, axis: .vertical)
                    .lineLimit(1...3)
                    .textFieldStyle(.plain)
                    .padding(10)
                    .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 10))
                    .submitLabel(.send)
                    .onSubmit { Task { await send() } }

                Button {
                    Task { await send() }
                } label: {
                    if sending {
                        ProgressView().tint(.white).frame(width: 44, height: 20)
                    } else {
                        Image(systemName: "arrow.up.circle.fill").font(.title2)
                    }
                }
                .disabled(sending || coordinatorURL == nil || prompt.trimmingCharacters(in: .whitespaces).isEmpty)
                .foregroundStyle(NodeStatus.live.color)
            }

            if let errorMsg {
                Label(errorMsg, systemImage: "exclamationmark.triangle.fill")
                    .font(.caption).foregroundStyle(.red)
            }

            if let reply {
                VStack(alignment: .leading, spacing: 8) {
                    Text(reply)
                        .font(.callout)
                        .foregroundStyle(.primary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if let stats, stats.tps != nil || stats.latency != nil {
                        HStack(spacing: 14) {
                            if let tps = stats.tps {
                                MetricChip(icon: "speedometer",
                                           text: String(format: "%.1f t/s", tps),
                                           color: NodeStatus.live.color)
                            }
                            if let latency = stats.latency {
                                MetricChip(icon: "clock",
                                           text: "\(latency) ms",
                                           color: .blue)
                            }
                        }
                    }
                }
                .padding(12)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(Color(.tertiarySystemFill), in: RoundedRectangle(cornerRadius: 10))
            }
        }
        .padding(16)
        .background(.regularMaterial, in: RoundedRectangle(cornerRadius: 14))
    }

    private var latestServedLabel: String? {
        guard reply != nil, let stats, stats.tps != nil else { return nil }
        return "measured this request"
    }

    private func send() async {
        guard let url = coordinatorURL, !sending else { return }
        let trimmed = prompt.trimmingCharacters(in: .whitespaces)
        guard !trimmed.isEmpty else { return }
        sending = true
        errorMsg = nil
        reply = nil
        stats = nil
        defer { sending = false }
        do {
            let result = try await NetworkClient.submitTestQuery(coordinatorURL: url, prompt: trimmed, userId: getOrCreateUserId())
            reply = result.content
            stats = (result.tokensPerSec, result.latencyMs)
        } catch {
            errorMsg = error.localizedDescription
        }
    }
}

private struct MetricChip: View {
    let icon: String
    let text: String
    let color: Color
    var body: some View {
        HStack(spacing: 4) {
            Image(systemName: icon).font(.system(size: 10))
            Text(text).font(.system(size: 12, weight: .semibold)).monospacedDigit()
        }
        .foregroundStyle(color)
    }
}
