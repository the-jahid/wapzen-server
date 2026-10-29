// Package activity records what users did — signed in, were shown a QR code,
// whether the scan worked. Each event is written to the application log and,
// once a Store is set, saved to the activity_logs table under its user.
//
// It has its own writer so these lines still print when the server's
// diagnostic logging (the standard logger) is switched off, which is the
// default outside development: see VERBOSE_LOGS in cmd/api.
package activity

import (
	"context"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"
)

// Event names what happened. It is stored as-is in activity_logs.event.
type Event string

const (
	SignedIn        Event = "signed_in"
	QRScanStarted   Event = "qr_scan_started"
	QRScanWorked    Event = "qr_scan_worked"
	QRScanFailed    Event = "qr_scan_failed"
	QRScanCancelled Event = "qr_scan_cancelled"
	// NumberUnlinked is a linked number removed because WhatsApp logged its
	// device out (unlinked on the phone, or replaced by a newer scan).
	NumberUnlinked Event = "number_unlinked"
)

// Entry is one thing a user did. Message is the line printed to the log;
// Detail is its variable part (a failure reason, a linked number), if any.
//
// QR scan entries also carry the attempt they belong to, so the store can keep
// one qr_scan_attempts row per QR code shown alongside the log lines.
type Entry struct {
	UserID  string
	Email   string
	Event   Event
	Message string
	Detail  string
	Attempt *QRAttempt
}

// QRAttempt identifies one QR login. ID is minted when the QR code is shown;
// PhoneNumberID, AgentID, Method and UserAgent are only read from the
// qr_scan_started entry.
//
// Method is how the user was asked to link: MethodQR (scan the code) or
// MethodPhoneCode (type a pairing code into WhatsApp). UserAgent is the browser
// that asked, so a failed attempt shows whether the user was on a phone.
type QRAttempt struct {
	ID            string
	PhoneNumberID string
	AgentID       string
	Method        string
	UserAgent     string
}

// Link methods stored in qr_scan_attempts.method.
const (
	MethodQR        = "qr"
	MethodPhoneCode = "phone_code"
)

// Store saves entries. The Repository in this package is the real one.
type Store interface {
	Insert(ctx context.Context, entry Entry) error
}

var (
	logger = log.New(os.Stderr, "", log.LstdFlags)

	storeMu sync.RWMutex
	store   Store
)

// saveTimeout bounds one insert. Saving runs off the request path, so a slow
// database delays nothing but the row itself.
const saveTimeout = 10 * time.Second

// SetOutput sends activity lines to w. Call it once at startup, before any
// request is served.
func SetOutput(w io.Writer) { logger.SetOutput(w) }

// UseStore makes Log save every entry to s as well as printing it.
func UseStore(s Store) {
	storeMu.Lock()
	defer storeMu.Unlock()
	store = s
}

// Printf writes a line that belongs to no user, such as the server starting.
// It is always printed and never saved.
func Printf(format string, args ...any) { logger.Printf(format, args...) }

// Log prints entry.Message and saves the entry in the background. An entry
// without a user is only printed, since every saved row belongs to one.
func Log(entry Entry) {
	logger.Print(entry.Message)

	storeMu.RLock()
	s := store
	storeMu.RUnlock()
	if s == nil || entry.UserID == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), saveTimeout)
		defer cancel()
		if err := s.Insert(ctx, entry); err != nil {
			logger.Printf("activity: could not save %s for %s: %v", entry.Event, entry.Email, err)
		}
	}()
}

type emailKey struct{}

// WithEmail records the signed-in user's email on a request context, so code
// further down (the WhatsApp login manager) can name the user in its lines
// without looking them up again.
func WithEmail(ctx context.Context, email string) context.Context {
	return context.WithValue(ctx, emailKey{}, email)
}

// Email is the email WithEmail stored, or "unknown" when there is none.
func Email(ctx context.Context) string {
	if email, ok := ctx.Value(emailKey{}).(string); ok && strings.TrimSpace(email) != "" {
		return email
	}
	return "unknown"
}

type userAgentKey struct{}

// WithUserAgent records the browser that made the request, for the same
// reason as WithEmail: the login manager saves it on the QR attempt.
func WithUserAgent(ctx context.Context, userAgent string) context.Context {
	return context.WithValue(ctx, userAgentKey{}, userAgent)
}

// UserAgent is the user agent WithUserAgent stored, or "" when there is none.
func UserAgent(ctx context.Context) string {
	userAgent, _ := ctx.Value(userAgentKey{}).(string)
	return strings.TrimSpace(userAgent)
}

// signInGap is how long a user has to be away before their next request counts
// as signing in again. The dashboard polls while it is open, so anything
// shorter than a real absence never trips it.
const signInGap = 30 * time.Minute

var (
	seenMu   sync.Mutex
	lastSeen = map[string]time.Time{}
)

// SeenUser logs "user signed in" the first time a user makes a request, and
// again whenever they come back after signInGap away. Clerk signs users in on
// the frontend, so the server's first sight of them is the closest it gets.
func SeenUser(userID, email string) {
	now := time.Now()
	seenMu.Lock()
	last, ok := lastSeen[userID]
	lastSeen[userID] = now
	seenMu.Unlock()
	if ok && now.Sub(last) < signInGap {
		return
	}
	Log(Entry{UserID: userID, Email: email, Event: SignedIn, Message: "user signed in: " + email})
}
