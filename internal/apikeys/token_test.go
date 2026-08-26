package apikeys

import "testing"

func TestGenerateCreatesRecognizableUniqueKey(t *testing.T) {
	first, err := Generate()
	if err != nil {
		t.Fatalf("Generate first key: %v", err)
	}
	second, err := Generate()
	if err != nil {
		t.Fatalf("Generate second key: %v", err)
	}

	if !LooksLikeKey(first) {
		t.Fatalf("generated key %q does not use expected prefix", first)
	}
	if first == second {
		t.Fatal("Generate returned the same key twice")
	}
	if Hash(first) == first {
		t.Fatal("Hash returned the plaintext key")
	}
	if Hash(first) != Hash(first) {
		t.Fatal("Hash is not deterministic")
	}
}

func TestPublicIdentifierRoundTrip(t *testing.T) {
	key := "wcai_abcdefghijklmnopqrstuvwxyz1234567890"
	prefix, suffix := splitPublicIdentifier(publicIdentifier(key))
	if prefix != publicPrefix(key) {
		t.Fatalf("prefix = %q, want %q", prefix, publicPrefix(key))
	}
	if suffix != last4(key) {
		t.Fatalf("suffix = %q, want %q", suffix, last4(key))
	}

	// Rows created directly against the latest schema may contain only a plain
	// prefix. They remain readable; the optional display suffix is simply empty.
	prefix, suffix = splitPublicIdentifier("wcai_legacy")
	if prefix != "wcai_legacy" || suffix != "" {
		t.Fatalf("plain prefix split = (%q, %q)", prefix, suffix)
	}
}
