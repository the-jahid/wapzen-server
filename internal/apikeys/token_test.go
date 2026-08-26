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
