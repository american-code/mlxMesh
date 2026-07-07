import SwiftUI

/// Deliberately does NOT expose --coordinator override, --tls-*,
/// --declared-memory-gb, --attempt-enclave-attestation, or
/// --refresh-interval — legitimate flags for power users on the CLI, not
/// for this simple UI (see the plan's explicit non-goals).
///
/// --reachability-endpoint WAS on that exclusion list originally but had to
/// come back: it auto-derives to "localhost:<port>" otherwise, which is
/// only ever correct if the coordinator happens to run on this same
/// machine. Confirmed live against the real production seed: a node with no
/// override set gets every real dispatch attempt refused by the coordinator
/// (it's trying to reach its own box, not this Mac) — this isn't a power-
/// user nicety for anyone behind NAT, which is effectively everyone.
struct SettingsSection: View {
    @Bindable var appState: AppState

    var body: some View {
        DisclosureGroup("Settings") {
            VStack(alignment: .leading, spacing: 10) {
                VStack(alignment: .leading, spacing: 2) {
                    Text("Share up to \(Int(appState.memoryCapPct * 100))% of this Mac's memory")
                        .font(.caption)
                    Slider(value: $appState.memoryCapPct, in: 0.1...0.9, step: 0.05)
                        .onChange(of: appState.memoryCapPct) { _, _ in appState.persistSettings() }
                }

                Picker("Region", selection: $appState.region) {
                    ForEach(AppState.Region.allCases) { region in
                        Text(region.displayName).tag(region)
                    }
                }
                .onChange(of: appState.region) { _, _ in appState.persistSettings() }

                Picker("Contribute", selection: $appState.scheduleMode) {
                    Text("Always").tag(AppState.ScheduleMode.always)
                    Text("Only during set hours").tag(AppState.ScheduleMode.window)
                }
                .pickerStyle(.segmented)
                .onChange(of: appState.scheduleMode) { _, _ in appState.persistSettings() }

                if appState.scheduleMode == .window {
                    HStack {
                        TextField("Start (HH:MM)", text: $appState.scheduleStart)
                            .onChange(of: appState.scheduleStart) { _, _ in appState.persistSettings() }
                        Text("–")
                        TextField("End (HH:MM)", text: $appState.scheduleEnd)
                            .onChange(of: appState.scheduleEnd) { _, _ in appState.persistSettings() }
                    }
                    .textFieldStyle(.roundedBorder)
                    .font(.caption)

                    HStack {
                        ForEach(AppState.Weekday.allCases) { day in
                            let isOn = appState.scheduleDays.contains(day)
                            Toggle(day.shortLabel, isOn: Binding(
                                get: { isOn },
                                set: { on in
                                    if on { appState.scheduleDays.insert(day) } else { appState.scheduleDays.remove(day) }
                                    appState.persistSettings()
                                }
                            ))
                            .toggleStyle(.button)
                            .controlSize(.mini)
                        }
                    }
                    Text(appState.scheduleDays.isEmpty ? "Every day" : "Selected days only")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }

                Divider()

                VStack(alignment: .leading, spacing: 2) {
                    Text("Reachable at (advanced)")
                        .font(.caption)
                    TextField("Leave blank unless behind a router/NAT", text: $appState.reachabilityEndpoint)
                        .onChange(of: appState.reachabilityEndpoint) { _, _ in appState.persistSettings() }
                        .textFieldStyle(.roundedBorder)
                        .font(.system(size: 11, design: .monospaced))
                        .autocorrectionDisabled()
                    Text("Only needed if this Mac isn't on the same network as the coordinator (true for the public mlxMesh network). Requires forwarding a port on your router to this Mac, then entering the public address here, e.g. http://your-address.example:8765 — otherwise the network can register this node but can never actually send it work.")
                        .font(.caption2)
                        .foregroundStyle(.secondary)
                }

                Toggle("Open at Login", isOn: Binding(
                    get: { appState.launchAtLogin.isEnabled },
                    set: { enabled in try? appState.launchAtLogin.setEnabled(enabled) }
                ))
                .font(.caption)
            }
            .padding(.top, 4)
        }
    }
}
