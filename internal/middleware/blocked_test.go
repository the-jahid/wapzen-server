package middleware

import "testing"

func TestLanguageForPhone(t *testing.T) {
	cases := map[string]string{
		"+962792010132":  "ar",
		"+212664252235":  "ar",
		"+22578490688":   "fr",
		"+6285183077233": "id",
		"+917007832324":  "hi",
		"+393314682549":  "it",
		"+14143495863":   "",
		"+94711436638":   "",
		"":               "",
	}
	for phone, want := range cases {
		if got := languageForPhone(phone); got != want {
			t.Errorf("languageForPhone(%q) = %q, want %q", phone, got, want)
		}
	}
}
