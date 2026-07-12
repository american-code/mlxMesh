// go_interop_helper is test-only tooling for the Python SDK's crypto interop
// test (see ../test_crypto.py). It exists to prove
// mlxmesh.crypto.encrypt_payload is byte-compatible with the real
// internal/payloadcrypto.Decrypt implementation, not just a from-memory
// reimplementation of the same algorithm. Lives inside this module's tree
// (no separate go.mod) so it can import the internal package; never
// imported by the mlxmesh package itself, never shipped in the PyPI sdist.
package main

import (
	"crypto/ecdh"
	"encoding/base64"
	"fmt"
	"os"

	"github.com/open-inference-mesh/oim/internal/payloadcrypto"
)

// args: <recipient-private-scalar-b64> <ephemeral-public-key-b64> <combined-ciphertext-b64>
// Prints the decrypted plaintext to stdout, or exits non-zero on failure.
func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: go_interop_helper <priv-scalar-b64> <ephemeral-pub-b64> <combined-b64>")
		os.Exit(2)
	}
	privRaw, err := base64.StdEncoding.DecodeString(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode private key:", err)
		os.Exit(1)
	}
	priv, err := ecdh.P256().NewPrivateKey(privRaw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "parse private key:", err)
		os.Exit(1)
	}
	ephemeralPub, err := base64.StdEncoding.DecodeString(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode ephemeral public key:", err)
		os.Exit(1)
	}
	combined, err := base64.StdEncoding.DecodeString(os.Args[3])
	if err != nil {
		fmt.Fprintln(os.Stderr, "decode ciphertext:", err)
		os.Exit(1)
	}
	pt, err := payloadcrypto.Decrypt(priv, ephemeralPub, combined)
	if err != nil {
		fmt.Fprintln(os.Stderr, "decrypt:", err)
		os.Exit(1)
	}
	fmt.Print(string(pt))
}
