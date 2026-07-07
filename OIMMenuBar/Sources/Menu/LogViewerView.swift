import SwiftUI

/// Plain, read-only scrolling tail of the node's recent stdout — no search,
/// filter, level-coloring, or export, per the plan's explicit non-goals.
struct LogViewerView: View {
    let lines: [String]

    var body: some View {
        DisclosureGroup("Log") {
            ScrollViewReader { proxy in
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 1) {
                        ForEach(Array(lines.enumerated()), id: \.offset) { index, line in
                            Text(line)
                                .font(.system(size: 10, design: .monospaced))
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .id(index)
                        }
                    }
                }
                .frame(height: 140)
                .background(.quaternary.opacity(0.2))
                .onChange(of: lines.count) { _, newCount in
                    proxy.scrollTo(newCount - 1, anchor: .bottom)
                }
            }
        }
    }
}
