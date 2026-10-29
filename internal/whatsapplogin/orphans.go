package whatsapplogin

import (
	"context"
	"log"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/store"
	"go.mau.fi/whatsmeow/types"

	"whatsapp-ai-caller-server/internal/activity"
)

// A WhatsApp phone takes at most four linked devices, and every companion this
// server pairs holds one of them until it is unlinked. A companion whose
// phone_numbers row is gone — a rescan the row could not be written for, a
// removal whose unlink did not reach WhatsApp — is used by nothing here, yet
// stays listed under Linked devices on the phone. Enough of them and the phone
// refuses the next scan, which on our side looks exactly like a QR code nobody
// scanned. The sweep unlinks them.
const orphanSweepInterval = 6 * time.Hour

// orphanConfirmWait separates the two looks a sweep takes. A pairing saves its
// device a moment before its row is written, and another process on the same
// database (a local dev server) may be in that moment, so only a companion that
// is still unaccounted for on the second look is unlinked.
const orphanConfirmWait = 2 * time.Minute

// SweepOrphanCompanions unlinks orphaned companions now and on every
// orphanSweepInterval until ctx ends. It runs in the background.
func (m *Manager) SweepOrphanCompanions(ctx context.Context) {
	if m == nil || m.container == nil {
		return
	}
	go func() {
		ticker := time.NewTicker(orphanSweepInterval)
		defer ticker.Stop()
		for {
			m.sweepOrphanCompanions(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (m *Manager) sweepOrphanCompanions(ctx context.Context) {
	first, err := m.orphanCompanions(ctx)
	if err != nil {
		log.Printf("whatsapp orphans: list failed: %v", err)
		return
	}
	if len(first) == 0 {
		return
	}

	select {
	case <-ctx.Done():
		return
	case <-time.After(orphanConfirmWait):
	}

	second, err := m.orphanCompanions(ctx)
	if err != nil {
		log.Printf("whatsapp orphans: list failed: %v", err)
		return
	}
	for jid, device := range second {
		if _, seen := first[jid]; !seen {
			continue
		}
		client := whatsmeow.NewClient(device, newWhatsAppLogger("", ""))
		// A phone that already dropped the companion fails the connect with a
		// logout, and whatsmeow deletes the stored device itself. Any other
		// failure keeps the device, so the next sweep tries again.
		if err := connectAndLogout(ctx, client); err != nil {
			log.Printf("whatsapp orphans: unlink failed wa_jid=%s: %v", jid, err)
			continue
		}
		activity.Printf("unlinked a leftover WhatsApp linked device with no phone number: %s", jid)
	}
}

// orphanCompanions returns the stored companions, by JID, that no
// phone_numbers row names and no live session in this process is using.
func (m *Manager) orphanCompanions(ctx context.Context) (map[string]*store.Device, error) {
	devices, err := m.container.GetAllDevices(ctx)
	if err != nil {
		return nil, err
	}
	recordedJIDs, err := m.repo.ListWAJIDs(ctx)
	if err != nil {
		return nil, err
	}

	accounted := make(map[string]struct{}, len(recordedJIDs))
	for _, raw := range recordedJIDs {
		if jid, err := types.ParseJID(strings.TrimSpace(raw)); err == nil {
			accounted[jid.String()] = struct{}{}
		}
	}
	m.mu.RLock()
	for _, session := range m.sessions {
		if id := session.client.Store.ID; id != nil {
			accounted[id.String()] = struct{}{}
		}
	}
	m.mu.RUnlock()

	out := make(map[string]*store.Device)
	for _, device := range devices {
		if device.ID == nil {
			continue
		}
		jid := device.ID.String()
		if _, ok := accounted[jid]; ok {
			continue
		}
		out[jid] = device
	}
	return out, nil
}
