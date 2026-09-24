// Package auth implements device-token registration and verification.
//
// Tokens are random 256-bit values shown to the user exactly once. Only an
// HMAC-SHA256 hash is persisted, so a database leak does not expose tokens.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/texhik/conclave/conclave-core/internal/domain"
	"github.com/texhik/conclave/conclave-core/internal/store"
)

// ErrUnauthorized is returned for missing, unknown or revoked tokens.
var ErrUnauthorized = errors.New("unauthorized")

// tokenPrefix identifies conclave-core device tokens.
const tokenPrefix = "cc_"

// userTokenPrefix identifies user-scoped tokens.
const userTokenPrefix = "cu_"

// Manager registers and authenticates devices.
type Manager struct {
	store  store.Store
	secret []byte
}

// New creates an auth manager. A non-empty secret is required.
func New(st store.Store, secret string) (*Manager, error) {
	if secret == "" {
		return nil, fmt.Errorf("auth: secret key must not be empty")
	}
	return &Manager{store: st, secret: []byte(secret)}, nil
}

// RegisterDevice creates a device and returns its plaintext token once.
func (m *Manager) RegisterDevice(ctx context.Context, tenantID, userID, name string) (string, domain.Device, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", domain.Device{}, err
	}
	token := tokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	dev := domain.Device{
		ID:        uuid.NewString(),
		TenantID:  tenantID,
		UserID:    userID,
		Name:      name,
		TokenHash: m.hash(token),
		CreatedAt: time.Now().UTC(),
	}
	if err := m.store.CreateDevice(ctx, dev); err != nil {
		return "", domain.Device{}, err
	}
	return token, dev, nil
}

// Authenticate verifies a token and returns the associated device.
func (m *Manager) Authenticate(ctx context.Context, token string) (*domain.Device, error) {
	if token == "" {
		return nil, ErrUnauthorized
	}
	dev, err := m.store.GetDeviceByTokenHash(ctx, m.hash(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	if dev.RevokedAt != nil {
		return nil, ErrUnauthorized
	}
	_ = m.store.TouchDevice(ctx, dev.ID)
	return dev, nil
}

// Revoke revokes a device.
func (m *Manager) Revoke(ctx context.Context, deviceID string) error {
	return m.store.RevokeDevice(ctx, deviceID)
}

// RegisterUserToken creates a user-scoped token and returns its plaintext once.
func (m *Manager) RegisterUserToken(ctx context.Context, tenantID, userID string) (string, domain.UserToken, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", domain.UserToken{}, err
	}
	token := userTokenPrefix + base64.RawURLEncoding.EncodeToString(raw)
	ut := domain.UserToken{
		ID:        uuid.NewString(),
		TenantID:  tenantID,
		UserID:    userID,
		TokenHash: m.hash(token),
		CreatedAt: time.Now().UTC(),
	}
	if err := m.store.CreateUserToken(ctx, ut); err != nil {
		return "", domain.UserToken{}, err
	}
	return token, ut, nil
}

// AuthenticateUser verifies a user token and returns it.
func (m *Manager) AuthenticateUser(ctx context.Context, token string) (*domain.UserToken, error) {
	if token == "" {
		return nil, ErrUnauthorized
	}
	ut, err := m.store.GetUserTokenByHash(ctx, m.hash(token))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, ErrUnauthorized
		}
		return nil, err
	}
	if ut.RevokedAt != nil {
		return nil, ErrUnauthorized
	}
	return ut, nil
}

func (m *Manager) hash(token string) string {
	mac := hmac.New(sha256.New, m.secret)
	mac.Write([]byte(token))
	return hex.EncodeToString(mac.Sum(nil))
}
