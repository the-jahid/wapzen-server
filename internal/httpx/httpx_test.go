package httpx

import (
	"reflect"
	"testing"

	"whatsapp-ai-caller-server/internal/swagger/modules/agents/examples"
)

const agentsPath = "/v1/agents"

// TestPaginationMetaMatchesDocumentedExample pins the meta block to the
// documented page-1-of-3 example (42 items, 20 per page).
func TestPaginationMetaMatchesDocumentedExample(t *testing.T) {
	meta := PaginationMeta(1, 20, 42)
	if !reflect.DeepEqual(meta, examples.PaginationMetaExample) {
		t.Errorf("meta = %#v, want %#v", meta, examples.PaginationMetaExample)
	}
}

// TestListLinksMatchesDocumentedExample pins the link block (no fields) to
// the documented example so the live route can't drift from the static contract.
func TestListLinksMatchesDocumentedExample(t *testing.T) {
	links := ListLinks(agentsPath, 1, 20, 42, nil)
	if !reflect.DeepEqual(links, examples.ListLinksExample) {
		t.Errorf("links = %#v, want %#v", links, examples.ListLinksExample)
	}
}

func TestListLinksCarriesFields(t *testing.T) {
	fields := []string{"id", "agent.status", "agent.language", "agent.call_direction"}
	links := ListLinks(agentsPath, 1, 20, 42, fields)

	wantSelf := "/v1/agents?page=1&limit=20&fields=id,agent.status,agent.language,agent.call_direction"
	if links.Self != wantSelf {
		t.Errorf("self = %q, want %q", links.Self, wantSelf)
	}
	if links.Previous != nil {
		t.Errorf("previous = %v, want nil on first page", *links.Previous)
	}
	if links.Next == nil {
		t.Fatal("next = nil, want a link on a non-final page")
	}
	wantNext := "/v1/agents?page=2&limit=20&fields=id,agent.status,agent.language,agent.call_direction"
	if *links.Next != wantNext {
		t.Errorf("next = %q, want %q", *links.Next, wantNext)
	}
}

func TestListLinksEmptyCollection(t *testing.T) {
	links := ListLinks(agentsPath, 1, 20, 0, nil)
	// With no items the Last link falls back to page 1 and there is no next/prev.
	if links.Last != "/v1/agents?page=1&limit=20" {
		t.Errorf("last = %q, want page 1 fallback", links.Last)
	}
	if links.Next != nil || links.Previous != nil {
		t.Errorf("expected no next/prev on empty collection, got next=%v prev=%v", links.Next, links.Previous)
	}
}
