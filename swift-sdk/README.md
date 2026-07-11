# MeshKit

Swift client SDK for [mlxMesh](https://github.com/open-inference-mesh/oim) —
a distributed AI inference network with dual-lane routing,
coordinator-verified earnings, and an optional end-to-end encrypted payload
path. Targets iOS 16+, macOS 13+, tvOS 16+, watchOS 9+.

```swift
.package(path: "swift-sdk")
// or, once tagged: .package(url: "https://github.com/open-inference-mesh/oim", from: "1.0.0")
```

## Quick start

```swift
import MeshKit

let client = MeshClient(baseURL: URL(string: "https://<coordinator>")!, apiKey: "<your-api-key>", userID: "<your-user-id>")

let response = try await client.chat(model: "llama-3.2-3b", messages: [ChatMessage(role: "user", content: "Summarize this: ...")])
print(response.content ?? "")
```

Setting `userID` isn't optional plumbing — it's what makes the coordinator
actually debit your account. Without it, requests run in the anonymous/
unmetered path.

## Streaming

```swift
for try await chunk in await client.streamChat(model: "llama-3.2-3b", messages: messages) {
    if let text = chunk.deltaContent { print(text, terminator: "") }
    if chunk.isUsageFrame { print("\n(usage: \(chunk.usage ?? [:]))") }
}
```

## Account

```swift
try await client.claimStartupGrant()   // mines the required proof-of-work nonce for you
let balance = try await client.balance()
```

## Background lane

Fast lane (`chat`/`streamChat`) and background lane are genuinely different
endpoints — background jobs are assigned once (sticky-session node
selection) then executed per recurrence cycle:

```swift
let job = try await client.submitBackgroundJob(
    model: "llama-3.2-3b", jobID: "daily-report",
    recurrence: RecurrenceSpec(intervalSeconds: 86400)
)
let result = try await client.runBackgroundCycle(job, messages: messages)
```

## Model discovery

The coordinator itself doesn't expose `/topology` — that's the separate
directory service, which tracks which pods currently serve which models:

```swift
let directory = MeshDirectory(baseURL: URL(string: "https://<directory>")!)
let pods = try await directory.findPods(modelID: "llama-3.2-3b")
```

## Privacy mode (encrypted-pointer)

For sensitive payloads, encrypt client-side to a specific reserved node's key
instead of sending plaintext:

```swift
let reservation = try await client.reserveNode(model: "llama-3.2-3b")
// host the encrypted bytes somewhere the assigned node can fetch them — the
// SDK does not solve hosting, that's application-specific
let response = try await client.submitEncrypted(reservation, messages: messages, fetchURL: yourHostedURL)
```

Not compatible with streaming — a reservation always returns buffered.

## Errors

```swift
do {
    try await client.chat(...)
} catch MeshError.insufficientCredits(_, let balance, let required) {
    print("need \(required), have \(balance)")
}
```

## Development

```bash
swift build
swift test
```

The cross-language crypto interop and live-mesh integration tests build and
run the real Go binaries from this repo — they skip cleanly (not a failure)
if the `go` toolchain isn't on `PATH`.
