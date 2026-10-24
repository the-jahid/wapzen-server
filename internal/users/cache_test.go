package users

import (
	"fmt"
	"testing"
	"time"

	"whatsapp-ai-caller-server/internal/models"
)

func TestUserCacheServesUntilExpiry(t *testing.T) {
	var c userCache
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	user := models.User{ID: "u1", OAuthID: "user_1", Email: "a@example.com"}

	if _, ok := c.get("user_1", now); ok {
		t.Fatal("empty cache returned a user")
	}

	c.put(user, now)
	got, ok := c.get("user_1", now.Add(userCacheTTL-time.Second))
	if !ok || got.ID != "u1" {
		t.Fatalf("get before expiry = %+v, %v; want u1, true", got, ok)
	}
	if _, ok := c.get("user_1", now.Add(userCacheTTL)); ok {
		t.Fatal("entry still served at its expiry")
	}
}

func TestUserCachePutReplacesAndDropRemoves(t *testing.T) {
	var c userCache
	now := time.Now()

	c.put(models.User{ID: "u1", OAuthID: "user_1", Email: "old@example.com"}, now)
	c.put(models.User{ID: "u1", OAuthID: "user_1", Email: "new@example.com"}, now)
	if got, _ := c.get("user_1", now); got.Email != "new@example.com" {
		t.Fatalf("email = %q after a second put, want the newer row", got.Email)
	}

	c.drop("user_1")
	if _, ok := c.get("user_1", now); ok {
		t.Fatal("dropped entry still served")
	}
	c.drop("user_never_cached") // must not panic
}

func TestUserCacheStaysBounded(t *testing.T) {
	var c userCache
	now := time.Now()

	// Fill the cache with entries that have all expired by "later".
	for i := range userCacheMaxEntries {
		c.put(models.User{ID: fmt.Sprint(i), OAuthID: fmt.Sprintf("user_%d", i)}, now)
	}
	later := now.Add(userCacheTTL)
	c.put(models.User{ID: "fresh", OAuthID: "user_fresh"}, later)

	if n := len(c.entries); n != 1 {
		t.Fatalf("entries = %d after sweeping expired ones, want 1", n)
	}
	if _, ok := c.get("user_fresh", later); !ok {
		t.Fatal("the entry that triggered the sweep was not kept")
	}

	// A full cache of live entries starts over rather than growing.
	clear(c.entries)
	for i := range userCacheMaxEntries {
		c.put(models.User{ID: fmt.Sprint(i), OAuthID: fmt.Sprintf("user_%d", i)}, now)
	}
	c.put(models.User{ID: "fresh", OAuthID: "user_fresh"}, now)
	if n := len(c.entries); n != 1 {
		t.Fatalf("entries = %d after overflowing with live entries, want 1", n)
	}
}
