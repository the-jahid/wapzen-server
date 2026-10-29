package whatsapplogin

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/types"

	"whatsapp-ai-caller-server/internal/models"
)

// relinkExistingNumber records a scan of a number the account already has as
// a relink rather than refusing it. People rescan a number they already have
// when it looks offline or stops answering, and the phone links the new device
// the moment they scan, so refusing it only leaves them a device that does
// nothing. Instead the existing row (its label, its agents, its history) moves
// onto the new device, the old device is unlinked from the phone, and the
// login's draft is promoted to that row so the page polling it sees the number
// connect. The caller then hands the number over (handOverRelinkedNumber).
func (m *Manager) relinkExistingNumber(session *loginSession, jid types.JID) (models.PhoneNumber, error) {
	phone := phoneNumberFromJID(jid)
	if phone == nil {
		return models.PhoneNumber{}, fmt.Errorf("paired device %s has no phone number", jid)
	}
	ctx := context.Background()

	session.persistMu.Lock()
	defer session.persistMu.Unlock()

	existing, err := m.repo.GetByUserPhone(ctx, session.userID, *phone)
	if err != nil {
		return models.PhoneNumber{}, err
	}

	// The old device stops serving the number before the row stops naming it,
	// so nothing it reports afterwards lands on the row.
	if old := m.getSession(existing.ID); old != nil {
		m.finishSession(old, false)
		go unlinkClient(old.client, existing.ID)
	} else if existing.WAJID != nil && strings.TrimSpace(*existing.WAJID) != jid.String() {
		go m.unlinkStoredDevice(existing)
	}

	row, err := m.repo.MarkConnected(ctx, session.userID, existing.ID, jid.String(), phone)
	if err != nil {
		return models.PhoneNumber{}, err
	}
	session.draft.promote(row)
	session.draft.end()

	// The agent whose editor started this login gets the number now, under the
	// row's own id. Spending assignOnce here also keeps the Connected that
	// follows from assigning the login's draft id, which names no row.
	if session.assignAgentID != "" && m.agentsRepo != nil {
		session.assignOnce.Do(func() {
			if _, err := m.agentsRepo.AssignPhoneNumberIfUnassigned(ctx, session.userID, session.assignAgentID, row.ID); err != nil {
				log.Printf("whatsapp login: assigning relinked phone_number_id=%s to agent %s failed: %v", row.ID, session.assignAgentID, err)
			}
		})
	}

	log.Printf("whatsapp login: relinked existing phone_number_id=%s onto wa_jid=%s (login %s)", row.ID, jid, session.phoneNumberID)
	return row, nil
}

// handOverRelinkedNumber moves a relinked number from the login session that
// paired it to a session of its own. The login session belongs to the draft's
// id, and its call handler was attached under it, so the number is resumed
// fresh under the row's id once WhatsApp's post-pairing reconnect is done —
// stopping the login earlier would race that reconnect for the device.
func (m *Manager) handOverRelinkedNumber(session *loginSession, row models.PhoneNumber) {
	session.client.WaitForConnection(30 * time.Second)
	m.finishSession(session, true)
	if err := m.resumeSession(context.Background(), row); err != nil {
		log.Printf("whatsapp login: resume relinked phone_number_id=%s failed: %v", row.ID, err)
	}
}

// unlinkStoredDevice unlinks the device a row named before it was relinked,
// when no session here is running it.
func (m *Manager) unlinkStoredDevice(row models.PhoneNumber) {
	client, err := m.clientFromStoredDevice(context.Background(), row)
	if err != nil || client == nil {
		if err != nil {
			log.Printf("whatsapp login: load replaced device failed phone_number_id=%s: %v", row.ID, err)
		}
		return
	}
	if err := connectAndLogout(context.Background(), client); err != nil {
		// Its row no longer names it, so the orphan sweep tries again.
		log.Printf("whatsapp login: unlink replaced device failed phone_number_id=%s: %v", row.ID, err)
	}
}

// unlinkClient logs a live client out, which unlinks its device from the
// phone, and drops the connection if that fails.
func unlinkClient(client *whatsmeow.Client, phoneNumberID string) {
	if err := logoutClient(context.Background(), client); err != nil {
		log.Printf("whatsapp login: unlink replaced device failed phone_number_id=%s: %v", phoneNumberID, err)
		client.Disconnect()
	}
}
