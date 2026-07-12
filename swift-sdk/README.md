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

let wallet = try Wallet.loadOrCreate()
let client = try MeshClient(baseURL: URL(string: "https://<coordinator>")!, wallet: wallet)

let response = try await client.chat(model: "llama-3.2-3b", messages: [ChatMessage(role: "user", content: "Summarize this: ...")])
print(response.content ?? "")
```

A billable call (`chat`, `streamChat`, `submitBackgroundJob`, ...) refuses
outright — before making any request — unless a credential is configured:
either a `wallet:` (see below) or a pre-existing `apiKey:`/`userID:` pair.
There is no anonymous/unmetered fallback path in this SDK; if you want the
coordinator to actually debit an account, a wallet is the way to get one.

## Wallets

A `Wallet` is a local Ed25519 keypair that proves ownership of a ledger
balance — portable across devices/processes and recoverable, unlike a random
per-session ID. The coordinator only ever sees the derived address and, once
per session, the public key + a signature; the private key never leaves
this process.

```swift
import MeshKit

// First run: generates a new keypair and saves it (0600 permissions) under
// the platform's Application Support directory. Later runs load the same
// one, so the address — and the balance tied to it — is stable across
// restarts. Pass your own WalletStorage (e.g. Keychain-backed) if you want
// different persistence.
let wallet = try Wallet.loadOrCreate()
print(wallet.address)  // "oim<64 hex chars>" — this is what the ledger keys balances on

let client = try MeshClient(baseURL: URL(string: "https://<coordinator>")!, wallet: wallet)
try await client.claimStartupGrant()

// The first billable call authenticates transparently: it signs a
// coordinator challenge with the wallet's key and mints a session apiKey —
// you never have to call this yourself, but you can:
let apiKey = try await client.authenticate()

let response = try await client.chat(model: "llama-3.2-3b", messages: [ChatMessage(role: "user", content: "...")])
```

If the coordinator ever rejects the current `apiKey` (expired, coordinator
restarted with a fresh in-memory store, etc.), the client re-authenticates
once automatically and retries — you don't need to handle 401s yourself as
long as a wallet is configured.

`Wallet.create()` generates a keypair without touching disk (you decide
if/where to persist it via `.save(storage:)`); `Wallet.load(storage:)` loads
an existing one and throws `WalletError.notFound` if there isn't one yet.

You can still pass a pre-existing `apiKey:`/`userID:` pair instead of a
wallet (e.g. a key you minted some other way) — the wallet flow is the
recommended default for new integrations, not the only supported one.

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
} catch MeshClientError.noCredentialConfigured {
    print("pass wallet: or apiKey:+userID: to MeshClient(...)")
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
