// Package examples holds typed sample payloads for the static Agents docs.
// Typing them against the types package keeps the examples structurally honest.
package examples

// strptr returns a pointer to s, for nullable string example fields.
func strptr(s string) *string { return &s }
