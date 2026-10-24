package whatsapplogin

import (
	"context"
	"log"
	"net/http"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
)

// WhatsApp refuses a companion that reports a web client version it has
// retired: the connect fails with 405 "client outdated" before any QR code is
// issued, so no number can be paired. The version compiled into whatsmeow goes
// stale within weeks, so the live one is read from web.whatsapp.com at startup
// and again on this interval for a process that stays up across a release.
const waVersionRefreshInterval = 6 * time.Hour

// keepWAVersionCurrent syncs the client version once before returning, so the
// connections opened right after startup already use it, then keeps it synced
// until ctx ends.
func keepWAVersionCurrent(ctx context.Context) {
	syncWAVersion(ctx)
	go func() {
		ticker := time.NewTicker(waVersionRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				syncWAVersion(ctx)
			}
		}
	}()
}

// syncWAVersion adopts the current web client version. A failed fetch keeps
// whatever version is already set rather than blocking startup.
func syncWAVersion(ctx context.Context) {
	fetchCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	latest, err := whatsmeow.GetLatestVersion(fetchCtx, http.DefaultClient)
	if err != nil {
		log.Printf("whatsapp version: fetch failed, keeping %s: %v", store.GetWAVersion(), err)
		return
	}
	// SetWAVersion writes package state that every connect reads, so it is only
	// touched when the version actually moved.
	if current := store.GetWAVersion(); *latest != current {
		store.SetWAVersion(*latest)
		log.Printf("whatsapp version: %s -> %s", current, latest)
	}
}
