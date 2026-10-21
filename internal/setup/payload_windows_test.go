//go:build windows

package setup

import (
	"os"
	"testing"
)

// Use the installer's own verification against the extracted delivery ZIP.
// This check reads the bundle only; it never creates services or databases.
func TestReleaseBundleIntegrity(t *testing.T) {
	bundle := os.Getenv("GMPAN_SETUP_VERIFY_BUNDLE")
	if bundle == "" {
		t.Skip("set GMPAN_SETUP_VERIFY_BUNDLE to an extracted complete setup bundle")
	}
	m, err := readManifest(bundle)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyPayload(bundle, m); err != nil {
		t.Fatal(err)
	}
}
