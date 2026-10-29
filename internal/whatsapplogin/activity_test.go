package whatsapplogin

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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

func TestQRTimeoutReasonTellsEarlyDisconnectFromExpiry(t *testing.T) {
	cases := []struct {
		byCode bool
		lasted time.Duration
		want   string
	}{
		{false, 161 * time.Second, "the QR code expired before it was scanned"},
		{true, 160 * time.Second, "the pairing code expired before it was entered"},
		{false, 42 * time.Second, "WhatsApp closed the connection before the code was used"},
		{true, 92 * time.Second, "WhatsApp closed the connection before the code was used"},
	}
	for _, tc := range cases {
		if got := qrTimeoutReason(tc.byCode, tc.lasted); got != tc.want {
			t.Errorf("qrTimeoutReason(%t, %s) = %q, want %q", tc.byCode, tc.lasted, got, tc.want)
		}
	}
}

func TestUniqueViolationIsRecognisedThroughWrapping(t *testing.T) {
	err := fmt.Errorf("create paired phone number: %w", &pgconn.PgError{Code: "23505"})
	if !isUniqueViolation(err) {
		t.Fatal("want a wrapped 23505 recognised as a unique violation")
	}
	if isUniqueViolation(errors.New("connection reset")) {
		t.Fatal("want a plain error not treated as a unique violation")
	}
}
