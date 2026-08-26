package phonenumbers

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"whatsapp-ai-caller-server/internal/models"
)

// ErrNotFound is returned when a phone number does not exist or is owned by
// another user.
var ErrNotFound = errors.New("phone number not found")

type dbQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Repository owns persistence for WhatsApp phone number login records.
type Repository struct {
	db dbQuerier
}

// NewRepository creates a phone number repository backed by pgxpool.
func NewRepository(pool *pgxpool.Pool) *Repository {
	return &Repository{db: pool}
}

const returningColumns = `
	id, user_id, phone_number, label, wa_jid, status, qr_code,
	last_connected_at, created_at, updated_at
`

// CreatePendingLogin inserts a user-owned phone number row in pending_qr state.
func (r *Repository) CreatePendingLogin(ctx context.Context, userID string, phoneNumber, label *string) (models.PhoneNumber, error) {
	const query = `
		INSERT INTO phone_numbers (user_id, phone_number, label, status)
		VALUES ($1, $2, $3, $4)
		RETURNING id, user_id, phone_number, label, wa_jid, status, qr_code,
			last_connected_at, created_at, updated_at
	`

	row, err := scanPhoneNumber(r.db.QueryRow(
		ctx,
		query,
		userID,
		normalizeOptional(phoneNumber),
		normalizeOptional(label),
		models.PhoneNumberStatusPendingQR,
	))
	if err != nil {
		return models.PhoneNumber{}, fmt.Errorf("create pending phone number login: %w", err)
	}
	return row, nil
}

// ListByUser returns the authenticated user's phone numbers, newest first.
func (r *Repository) ListByUser(ctx context.Context, userID string) ([]models.PhoneNumber, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM phone_numbers
		WHERE user_id = $1
		ORDER BY created_at DESC, id DESC
	`, returningColumns)

	rows, err := r.db.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("list phone numbers: %w", err)
	}
	defer rows.Close()

	out := make([]models.PhoneNumber, 0)
	for rows.Next() {
		phoneNumber, err := scanPhoneNumber(rows)
		if err != nil {
			return nil, fmt.Errorf("scan phone number: %w", err)
		}
		out = append(out, phoneNumber)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate phone numbers: %w", err)
	}
	return out, nil
}

// ListConnected returns every connected phone number, regardless of owner.
// It is intended for system-level background processes that have no user scope.
func (r *Repository) ListConnected(ctx context.Context) ([]models.PhoneNumber, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM phone_numbers
		WHERE status = $1
		ORDER BY created_at DESC, id DESC
	`, returningColumns)

	rows, err := r.db.Query(ctx, query, models.PhoneNumberStatusConnected)
	if err != nil {
		return nil, fmt.Errorf("list connected phone numbers: %w", err)
	}
	defer rows.Close()

	out := make([]models.PhoneNumber, 0)
	for rows.Next() {
		phoneNumber, err := scanPhoneNumber(rows)
		if err != nil {
			return nil, fmt.Errorf("scan connected phone number: %w", err)
		}
		out = append(out, phoneNumber)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate connected phone numbers: %w", err)
	}
	return out, nil
}

// ListResumable returns every phone number that still has valid stored WhatsApp
// credentials worth reconnecting on startup: both currently-connected rows and
// rows that merely lost their live socket (disconnected). Rows in failed/expired
// never had a working device, so they are intentionally excluded.
func (r *Repository) ListResumable(ctx context.Context) ([]models.PhoneNumber, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM phone_numbers
		WHERE status = ANY($1)
		ORDER BY created_at DESC, id DESC
	`, returningColumns)

	statuses := []string{
		models.PhoneNumberStatusConnected,
		models.PhoneNumberStatusDisconnected,
	}
	rows, err := r.db.Query(ctx, query, statuses)
	if err != nil {
		return nil, fmt.Errorf("list resumable phone numbers: %w", err)
	}
	defer rows.Close()

	out := make([]models.PhoneNumber, 0)
	for rows.Next() {
		phoneNumber, err := scanPhoneNumber(rows)
		if err != nil {
			return nil, fmt.Errorf("scan resumable phone number: %w", err)
		}
		out = append(out, phoneNumber)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate resumable phone numbers: %w", err)
	}
	return out, nil
}

// GetByUser loads one phone number by id, scoped to the authenticated user.
func (r *Repository) GetByUser(ctx context.Context, userID, phoneNumberID string) (models.PhoneNumber, error) {
	query := fmt.Sprintf(`
		SELECT %s
		FROM phone_numbers
		WHERE id = $1 AND user_id = $2
	`, returningColumns)

	row, err := scanPhoneNumber(r.db.QueryRow(ctx, query, phoneNumberID, userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.PhoneNumber{}, ErrNotFound
		}
		return models.PhoneNumber{}, fmt.Errorf("get phone number: %w", err)
	}
	return row, nil
}

// SetQRCode stores the latest renderable QR data URL for a pending login.
func (r *Repository) SetQRCode(ctx context.Context, userID, phoneNumberID, qrCode string) (models.PhoneNumber, error) {
	const query = `
		UPDATE phone_numbers
		SET qr_code = $3, status = $4, updated_at = now()
		WHERE id = $1 AND user_id = $2
		RETURNING id, user_id, phone_number, label, wa_jid, status, qr_code,
			last_connected_at, created_at, updated_at
	`

	return r.scanUpdated(ctx, query, phoneNumberID, userID, qrCode, models.PhoneNumberStatusPendingQR)
}

// MarkConnected stores the paired WhatsApp JID and transitions the row to
// connected.
func (r *Repository) MarkConnected(ctx context.Context, userID, phoneNumberID, waJID string, phoneNumber *string) (models.PhoneNumber, error) {
	const query = `
		UPDATE phone_numbers
		SET wa_jid = $3,
			phone_number = COALESCE($4, phone_number),
			status = $5,
			qr_code = NULL,
			last_connected_at = now(),
			updated_at = now()
		WHERE id = $1 AND user_id = $2
		RETURNING id, user_id, phone_number, label, wa_jid, status, qr_code,
			last_connected_at, created_at, updated_at
	`

	return r.scanUpdated(ctx, query, phoneNumberID, userID, waJID, normalizeOptional(phoneNumber), models.PhoneNumberStatusConnected)
}

// ResetPendingLogin returns an existing user-owned phone number row to the
// pending_qr state so a fresh QR login session can be started for it.
func (r *Repository) ResetPendingLogin(ctx context.Context, userID, phoneNumberID string) (models.PhoneNumber, error) {
	return r.markStatus(ctx, userID, phoneNumberID, models.PhoneNumberStatusPendingQR)
}

// ResetForRePair clears the old companion JID while preserving the phone-number
// row and any agent assignment that references it.
func (r *Repository) ResetForRePair(ctx context.Context, userID, phoneNumberID string) (models.PhoneNumber, error) {
	const query = `
		UPDATE phone_numbers
		SET wa_jid = NULL,
			status = $3,
			qr_code = NULL,
			updated_at = now()
		WHERE id = $1 AND user_id = $2
		RETURNING id, user_id, phone_number, label, wa_jid, status, qr_code,
			last_connected_at, created_at, updated_at
	`

	return r.scanUpdated(ctx, query, phoneNumberID, userID, models.PhoneNumberStatusPendingQR)
}

func (r *Repository) MarkFailed(ctx context.Context, userID, phoneNumberID string) (models.PhoneNumber, error) {
	return r.markStatus(ctx, userID, phoneNumberID, models.PhoneNumberStatusFailed)
}

func (r *Repository) MarkExpired(ctx context.Context, userID, phoneNumberID string) (models.PhoneNumber, error) {
	return r.markStatus(ctx, userID, phoneNumberID, models.PhoneNumberStatusExpired)
}

func (r *Repository) MarkDisconnected(ctx context.Context, userID, phoneNumberID string) (models.PhoneNumber, error) {
	return r.markStatus(ctx, userID, phoneNumberID, models.PhoneNumberStatusDisconnected)
}

// DeleteByUser permanently removes one phone number row owned by the
// authenticated user.
func (r *Repository) DeleteByUser(ctx context.Context, userID, phoneNumberID string) (models.PhoneNumber, error) {
	query := fmt.Sprintf(`
		DELETE FROM phone_numbers
		WHERE id = $1 AND user_id = $2
		RETURNING %s
	`, returningColumns)

	row, err := scanPhoneNumber(r.db.QueryRow(ctx, query, phoneNumberID, userID))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.PhoneNumber{}, ErrNotFound
		}
		return models.PhoneNumber{}, fmt.Errorf("delete phone number: %w", err)
	}
	return row, nil
}

func (r *Repository) markStatus(ctx context.Context, userID, phoneNumberID, status string) (models.PhoneNumber, error) {
	const query = `
		UPDATE phone_numbers
		SET status = $3, qr_code = NULL, updated_at = now()
		WHERE id = $1 AND user_id = $2
		RETURNING id, user_id, phone_number, label, wa_jid, status, qr_code,
			last_connected_at, created_at, updated_at
	`

	return r.scanUpdated(ctx, query, phoneNumberID, userID, status)
}

func (r *Repository) scanUpdated(ctx context.Context, query string, args ...any) (models.PhoneNumber, error) {
	row, err := scanPhoneNumber(r.db.QueryRow(ctx, query, args...))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return models.PhoneNumber{}, ErrNotFound
		}
		return models.PhoneNumber{}, fmt.Errorf("update phone number: %w", err)
	}
	return row, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanPhoneNumber(row rowScanner) (models.PhoneNumber, error) {
	var phoneNumber models.PhoneNumber
	if err := row.Scan(
		&phoneNumber.ID,
		&phoneNumber.UserID,
		&phoneNumber.PhoneNumber,
		&phoneNumber.Label,
		&phoneNumber.WAJID,
		&phoneNumber.Status,
		&phoneNumber.QRCode,
		&phoneNumber.LastConnectedAt,
		&phoneNumber.CreatedAt,
		&phoneNumber.UpdatedAt,
	); err != nil {
		return models.PhoneNumber{}, err
	}
	return phoneNumber, nil
}

func normalizeOptional(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}
