package users

import (
	"sync"
	"time"

	"whatsapp-ai-caller-server/internal/models"
)

// Every authenticated request resolves its Clerk user to a row here before the
// handler runs, which made the lookup a database round trip on every API call,
// in series with the handler's own queries. A row only changes through Upsert
// and DeleteByClerkID, which keep the cache in step, so serving it from memory
// for a few minutes is safe; the TTL bounds how long another instance's write
// can take to show.
const (
	userCacheTTL = 5 * time.Minute
	// userCacheMaxEntries caps memory: at the cap, expired entries are swept and
	// if that frees nothing the cache starts over.
	userCacheMaxEntries = 10_000
)

// userCache maps a Clerk user id to its user row. The zero value is ready to
// use.
type userCache struct {
	mu      sync.Mutex
	entries map[string]cachedUser
}

type cachedUser struct {
	user    models.User
	expires time.Time
}

func (c *userCache) get(clerkID string, now time.Time) (models.User, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[clerkID]
	if !ok || !now.Before(entry.expires) {
		return models.User{}, false
	}
	return entry.user, true
}

func (c *userCache) put(user models.User, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.entries == nil {
		c.entries = make(map[string]cachedUser)
	}
	if _, ok := c.entries[user.OAuthID]; !ok && len(c.entries) >= userCacheMaxEntries {
		for id, entry := range c.entries {
			if !now.Before(entry.expires) {
				delete(c.entries, id)
			}
		}
		if len(c.entries) >= userCacheMaxEntries {
			clear(c.entries)
		}
	}
	c.entries[user.OAuthID] = cachedUser{user: user, expires: now.Add(userCacheTTL)}
}

func (c *userCache) drop(clerkID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, clerkID)
}
