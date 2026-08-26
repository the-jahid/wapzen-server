package constants

// concat returns a new slice containing every element of the inputs, used to
// build union option lists without aliasing the source slices.
func concat(parts ...[]string) []string {
	out := make([]string, 0)
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
