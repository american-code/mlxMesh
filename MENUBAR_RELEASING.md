# Releasing the mlxMesh Menu-Bar App (OIMMenuBar)

This is the release process for the macOS menu-bar node app —
**a separate concern from [RELEASING.md](RELEASING.md)**, not a duplicate of
it. `RELEASING.md` signs the `oim` **binary's checksum manifest** with cosign/
Sigstore for supply-chain provenance ("was this binary really built from the
tagged commit?"). This document signs **the app bundle itself** with a
Developer ID certificate and notarizes it with Apple, so Gatekeeper lets a
user open a downloaded `.app` without a security warning ("did Apple vet
this app, and is it exactly what the developer built?"). Different threat
model, different tooling, different credentials — don't conflate them.

## What a release produces

- A notarized, stapled `mlxMesh.app`, packaged as `mlxMesh.dmg`.
- The embedded `oim` binary inside it reports its own version via
  `oim version` — stamped via the same `internal/version` ldflags convention
  `RELEASING.md`'s binaries use, so "which oim is inside this app build" is
  never a guess.

## Why this needs its own pipeline, not TestFlight's

`.github/workflows/testflight.yml` + `fastlane/` are wired for **Apple
Distribution / App Store Connect** signing — the right model for an app
submitted to TestFlight/the App Store. A menu-bar utility distributed
directly (DMG download, not the App Store) needs **Developer ID Application
signing + `xcrun notarytool`** instead — a disjoint certificate type and a
disjoint credential flow. Mixing the two pipelines risks a silent
wrong-signing-method mistake (e.g. exporting this app with `app-store` method,
which produces something Gatekeeper won't trust outside the Store). See
`.github/workflows/menubar-release.yml`.

## Build order (must be exact)

`xcodegen` auto-discovers the embedded `oim` binary as a bundle resource by
scanning the filesystem *at generation time* — so the binary has to exist on
disk before the project is generated:

```
OIMMenuBar/scripts/embed-oim.sh   # builds OIMMenuBar/Resources/oim
cd OIMMenuBar && xcodegen generate
xcodebuild ...
```

Running these out of order produces a project with no reference to the
binary at all (silently missing from the built app, not a build error) —
verify with `ls OIMMenuBar/Resources/oim` before generating if something
seems off.

## Cutting a release

1. **Pre-flight** (no signing needed, safe to run anytime):
   ```
   OIMMenuBar/scripts/embed-oim.sh
   cd OIMMenuBar && xcodegen generate
   xcodebuild build -scheme OIMMenuBar -destination 'generic/platform=macOS' CODE_SIGNING_ALLOWED=NO
   ```
   Or trigger `menubar-release.yml` with `lane: preflight`.
2. **Archive with Developer ID signing:**
   ```
   xcodebuild archive -project OIMMenuBar/OIMMenuBar.xcodeproj -scheme OIMMenuBar \
     -archivePath build/OIMMenuBar.xcarchive \
     CODE_SIGN_IDENTITY="Developer ID Application" DEVELOPMENT_TEAM=<team-id>
   ```
3. **Sign the embedded binary too — not just the app wrapper.** Notarization
   requires *every* executable inside the bundle to carry a valid signature,
   not only the top-level `.app`:
   ```
   codesign --force --options runtime --sign "Developer ID Application" OIMMenuBar.app/Contents/Resources/oim
   codesign --force --deep --options runtime --sign "Developer ID Application" OIMMenuBar.app
   ```
4. **Notarize and staple:**
   ```
   xcrun notarytool submit OIMMenuBar.app --key <notary-key.p8> --key-id <key-id> --issuer <issuer-id> --wait
   xcrun stapler staple OIMMenuBar.app
   ```
5. **Package as a `.dmg`** (draggable-to-Applications, the expected UX for
   this class of app — not a bare `.zip`):
   ```
   mkdir dmg-root && cp -R OIMMenuBar.app dmg-root/ && ln -s /Applications dmg-root/Applications
   hdiutil create -volname "mlxMesh" -srcfolder dmg-root -ov -format UDZO mlxMesh.dmg
   ```
6. **Publish** `mlxMesh.dmg` to a GitHub Release / the landing page.

All of the above is scripted in `.github/workflows/menubar-release.yml`'s
`release` lane — the manual steps here exist so the process is understandable
without reading YAML, per this repo's existing docs convention.

## Credentials the operator must provision (not yet configured)

- **`OIM_DEVELOPER_ID_CERT`** — base64 of the exported Developer ID
  Application `.p12`, and **`OIM_DEVELOPER_ID_CERT_PASSWORD`** — its export
  password. Generate in the Apple Developer portal → Certificates → Developer
  ID Application, export from Keychain Access as a `.p12`.
- **`OIM_DEVELOPER_ID_TEAM_ID`** — likely `WXDFFW3882` (the same paid Apple
  Developer Program membership already implied by the TestFlight scaffold),
  but confirm rather than assume — Developer ID and App Store Connect
  provisioning are configured semi-independently in the portal.
- **`OIM_NOTARY_API_KEY_ID`** / **`OIM_NOTARY_API_ISSUER_ID`** /
  **`OIM_NOTARY_API_KEY_CONTENT`** — an App Store Connect API key scoped for
  notarization. **This is a different key from the TestFlight ASC secrets**
  (`OIM_ASC_KEY_ID` etc.) — do not reuse those as-is without checking their
  scope; generate a dedicated key if unsure.

Store the `.p12` and notary key file securely (password manager / local
Keychain), never committed to the repo — same principle as every other
secret in this project (`SECURITY.md`, `RUNBOOK.md`'s secrets-rotation
section).

## Verifying a downloaded release

```
spctl -a -vv mlxMesh.app        # Gatekeeper: should say "accepted", source = Developer ID
xcrun stapler validate mlxMesh.app
mlxMesh.app/Contents/Resources/oim version   # confirm the embedded binary's stamped version
```
