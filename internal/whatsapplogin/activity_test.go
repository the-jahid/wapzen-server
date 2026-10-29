package whatsapplogin

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"whatsapp-ai-caller-server/internal/activity"
)

func TestScanOutcomeLogsOncePerQRLogin(t *testing.T) {
	var out bytes.Buffer
	activity.SetOutput(&out)

	session := &loginSession{email: "user@example.com", qrShown: true}
	session.logScanFailed("this number is already connected to their account")
	session.logScanFailed("second report of the same scan")

	got := out.String()
	if strings.Count(got, "QR scan failed") != 1 {
		t.Fatalf("want exactly one outcome line, got %q", got)
	}
	if !strings.Contains(got, "user@example.com tried to scan but it did not work (this number is already connected to their account)") {
		t.Fatalf("outcome line missing email or reason: %q", got)
	}
}

func TestScanOutcomeSkipsLoginsWithoutQR(t *testing.T) {
	var out bytes.Buffer
	activity.SetOutput(&out)

	// A relink of an already-paired device never shows a QR code.
	session := &loginSession{email: "user@example.com"}
	session.logScanFailed("client outdated")

	if out.Len() != 0 {
		t.Fatalf("want no line for a login without a QR code, got %q", out.String())
	}
}

func TestScanFailureReasonNamesDuplicateNumber(t *testing.T) {
	err := fmt.Errorf("insert: %w", &pgconn.PgError{Code: "23505"})
	if got := scanFailureReason(err); got != "this number is already connected to their account" {
		t.Fatalf("scanFailureReason = %q", got)
	}
	if got := scanFailureReason(errors.New("boom")); got != "boom" {
		t.Fatalf("scanFailureReason = %q", got)
	}
}
