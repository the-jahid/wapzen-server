package broadcasts

import (
	"net/url"
	"strings"
	"testing"
)

func TestNormalizePhoneNumber(t *testing.T) {
	cases := map[string]string{
		"+880 1712-345678":    "+8801712345678",
		" 8801712345678 ":     "+8801712345678",
		"00 55 11 98765-4321": "+5511987654321",
		"+1 (555) 765.4321":   "+15557654321",
		"":                    "",
		"12345":               "",
		"+0123456789":         "",
		"+88017abc45678":      "",
		"+1234567890123456":   "",
	}
	for in, want := range cases {
		got, ok := NormalizePhoneNumber(in)
		if want == "" {
			if ok {
				t.Errorf("NormalizePhoneNumber(%q) = %q, want rejected", in, got)
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("NormalizePhoneNumber(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

func TestParseContactQuery(t *testing.T) {
	q, msg := parseContactQuery(url.Values{})
	if msg != "" || q != (ContactQuery{Page: 1, Limit: 10, Sort: "created", Desc: true}) {
		t.Errorf("defaults = %+v, %q", q, msg)
	}
	q, msg = parseContactQuery(url.Values{"page": {"3"}, "limit": {"25"}, "search": {" Nadia "}, "sort": {"name"}, "order": {"asc"}})
	if msg != "" || q != (ContactQuery{Page: 3, Limit: 25, Search: "Nadia", Sort: "name"}) {
		t.Errorf("full query = %+v, %q", q, msg)
	}
	for _, bad := range []url.Values{
		{"page": {"0"}}, {"page": {"x"}}, {"page": {"99999999999"}}, {"limit": {"0"}}, {"limit": {"101"}},
		{"sort": {"id; DROP TABLE x"}}, {"order": {"up"}}, {"search": {strings.Repeat("a", 201)}},
	} {
		if _, msg := parseContactQuery(bad); msg == "" {
			t.Errorf("parseContactQuery(%v) accepted", bad)
		}
	}
}
