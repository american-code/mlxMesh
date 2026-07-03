# train-router — on-device intent router pipeline

Produces the signed Core ML model that `OIMDashboard`'s `CoreMLRouter` loads.
Runs entirely on a Mac — no iOS device needed. Step 10 of the on-device routing
spec was never device-blocked; it was **data**-blocked (see the roadmap below).

## Build a model

```sh
OIM_MODEL_SIGNING_KEY=<base64 ed25519 private key> \
  swift tools/train-router/main.swift <output-dir>
```

Outputs, co-located, matching `ModelSignatureVerifier`'s conventions:

- `IntentClassifier.mlmodel` — single-file MaxEnt text classifier (~7.5 KB).
  Predicts a composite `label` of the form `jobType|sensitivity|modelFamily`.
- `IntentClassifier.mlmodel.sig` — Ed25519 signature over `SHA-256(model)`.
- `IntentClassifier.mlmodel.version` — e.g. `core_ml_v1`.

Drop these three files into the iOS app bundle. `CoreMLRouter` verifies the
signature **before** Core ML touches the file, compiles at load time, and
prediction failures fall back to `RuleBasedRouter`. The signing **public** key
is embedded in `OIMDashboard/iOS/Routing/ModelSignatureVerifier.swift`; the
matching **private** key must live only in the release environment
(`OIM_MODEL_SIGNING_KEY`), never in the app or this repo.

## Validated device-free

The full path — train → sign → `ModelSignatureVerifier.verify` → compile →
predict → composite-label split → tamper-rejection — was compiled and run on
macOS. A tampered model is rejected; a signed one verifies and predicts (SQL →
`query_optimization`, SSN text → `high_requires_attestation`).

## Honest scope, and the self-learning roadmap

**v1 (this pipeline) is templated data**, so the model largely reproduces the
`RuleBasedRouter` keyword rules. It validates the whole ML plumbing with a real
signed artifact; it does **not** add real intelligence. `RuleBasedRouter` stays
the runtime default until a smarter model exists. Do not present v1 as a
fine-tuned intent classifier.

**v2 — on-device teacher-student distillation (needs a device to validate).**
The key insight: an iPad running Exo already has a capable LLM on the device.
Use it as the **teacher** to label recent prompts (job type / sensitivity /
model family); train a fast, updatable Core ML **student** on those labels via
`MLUpdateTask`. Everything stays on the device, so the privacy constraint (raw
prompts never leave) holds. Guardrails: keep the teacher local; only updatable
Core ML model types can learn on-device (kNN or a last-layer head over frozen
embeddings — not a full transformer fine-tune); fold in the coordinator's
`RecordHintAccuracy` signal carefully; hold out an eval set and auto-fall-back
to `RuleBasedRouter` if an update regresses; never let a learned model
de-escalate sensitivity below the rule floor.

**v3 — optional privacy-preserving central re-distillation.** Periodically
distill the best on-device students into a stronger shipped baseline, without
raw prompts leaving devices. Its own project; revisit only with a dedicated
privacy pass.
