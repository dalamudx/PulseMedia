package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dalamudx/PulseMedia/internal/ports"
)

const (
	SetupAdvisoryLockID = 0x50756c736553 // "PulseS"; retained for public compatibility.
	SessionDuration     = 7 * 24 * time.Hour
)

type Service struct {
	repo      ports.AuthenticationRepository
	secrets   ports.AuthenticationSecrets
	clock     ports.Clock
	idgen     ports.IDGen
	mfaCipher ports.SettingsCipher
}

func NewService(repo ports.AuthenticationRepository, secrets ports.AuthenticationSecrets, clock ports.Clock, idgen ports.IDGen) *Service {
	return &Service{repo: repo, secrets: secrets, clock: clock, idgen: idgen}
}

func (s *Service) IsInitialized(ctx context.Context) (bool, error) {
	return s.repo.IsInitialized(ctx)
}

// SetupAdmin validates the initial identity and atomically issues its first session.
func (s *Service) SetupAdmin(ctx context.Context, username, password string) (*ports.User, string, error) {
	username = strings.TrimSpace(username)
	if len(username) < 3 {
		return nil, "", errors.New("username must be at least 3 characters")
	}
	if len(password) < 8 {
		return nil, "", errors.New("password must be at least 8 characters")
	}
	initialized, err := s.repo.IsInitialized(ctx)
	if err != nil {
		return nil, "", err
	}
	if initialized {
		return nil, "", ports.ErrAlreadyInitialized
	}
	// Password work finishes before opening the issuance transaction.
	passHash, err := s.secrets.HashPassword(password)
	if err != nil {
		return nil, "", fmt.Errorf("failed to hash password: %w", err)
	}
	var user ports.User
	var plain string
	err = s.repo.WithAdminSetup(ctx, func(tx ports.AuthenticationSetup) error {
		initialized, err := tx.IsInitialized(ctx)
		if err != nil {
			return err
		}
		if initialized {
			return ports.ErrAlreadyInitialized
		}
		user, err = tx.CreateAdmin(ctx, s.idgen.NewUUID(), username, passHash)
		if err != nil {
			return err
		}
		plain, err = s.issueSession(ctx, tx, user)
		return err
	})
	if err != nil {
		return nil, "", err
	}
	return &user, plain, nil
}

// Login checks the password outside the transaction, then revalidates the locked
// account before issuing a session. The current role is returned to the caller.
func (s *Service) Login(ctx context.Context, username, password string, factors ...ports.MFAProof) (*ports.User, string, error) {
	if len(username) > 80 || len(password) > 1024 || len(factors) > 1 {
		return nil, "", ports.ErrInvalidCredentials
	}
	proof := ports.MFAProof{}
	if len(factors) == 1 {
		proof = factors[0]
	}
	if !validProof(proof) {
		return nil, "", ports.ErrInvalidCredentials
	}
	account, err := s.repo.FindAccount(ctx, strings.TrimSpace(username))
	if errors.Is(err, ports.ErrAuthenticationNotFound) {
		return nil, "", ports.ErrInvalidCredentials
	}
	if err != nil {
		return nil, "", err
	}
	if !account.User.IsActive {
		return nil, "", ports.ErrUserDisabled
	}
	if !s.secrets.VerifyPassword(password, account.PasswordHash) {
		return nil, "", ports.ErrInvalidCredentials
	}
	var user ports.User
	var plain string
	err = s.repo.WithLoginAccount(ctx, account.User.ID, func(current ports.AuthenticationAccount, tx ports.AuthenticationSessionWriter) error {
		if !current.User.IsActive {
			return ports.ErrUserDisabled
		}
		if current.PasswordHash != account.PasswordHash {
			return ports.ErrInvalidCredentials
		}
		if mfa, ok := tx.(ports.MFATransaction); ok {
			if e := verifyMFA(ctx, mfa, s.mfaCipher, s.secrets, s.clock.Now(), proof); e != nil {
				return e
			}
		}
		user = current.User
		var err error
		plain, err = s.issueSession(ctx, tx, user)
		return err
	})
	if errors.Is(err, ports.ErrAuthenticationNotFound) {
		return nil, "", ports.ErrInvalidCredentials
	}
	if err != nil {
		return nil, "", err
	}
	return &user, plain, nil
}

func (s *Service) issueSession(ctx context.Context, tx ports.AuthenticationSessionWriter, user ports.User) (string, error) {
	plain, hash, err := s.secrets.GenerateToken("pm_sess_")
	if err != nil {
		return "", fmt.Errorf("failed to generate session token: %w", err)
	}
	_, csrfHash, err := s.secrets.GenerateToken("pm_csrf_")
	if err != nil {
		return "", fmt.Errorf("failed to generate csrf token: %w", err)
	}
	err = tx.CreateSession(ctx, ports.Session{
		ID: s.idgen.NewUUID(), UserID: user.ID, TokenHash: hash, CsrfTokenHash: csrfHash,
		ExpiresAt: s.clock.Now().Add(SessionDuration),
	})
	if err != nil {
		return "", err
	}
	return plain, nil
}

// AuthenticateToken resolves a cookie or bearer credential using current account facts.
func (s *Service) AuthenticateToken(ctx context.Context, rawToken string) (*ports.Actor, error) {
	if rawToken == "" {
		return nil, ports.ErrUnauthorized
	}
	hash := s.secrets.HashToken(rawToken)
	sess, err := s.repo.FindSession(ctx, hash)
	if err == nil {
		if !sess.User.IsActive {
			return nil, ports.ErrUserDisabled
		}
		if !sess.ExpiresAt.After(s.clock.Now()) {
			return nil, ports.ErrTokenExpired
		}
		_ = s.repo.TouchSession(ctx, sess.ID)
		return &ports.Actor{Type: ports.ActorTypeUser, ID: sess.User.ID.String(), Name: sess.User.Username, Role: sess.User.Role}, nil
	}
	if !errors.Is(err, ports.ErrAuthenticationNotFound) {
		return nil, err
	}
	tok, err := s.repo.FindAPIToken(ctx, hash)
	if err == nil {
		if !tok.User.IsActive {
			return nil, ports.ErrUserDisabled
		}
		if tok.RevokedAt != nil {
			return nil, ports.ErrTokenRevoked
		}
		if tok.ExpiresAt != nil && !tok.ExpiresAt.After(s.clock.Now()) {
			return nil, ports.ErrTokenExpired
		}
		scopes := make([]string, 0)
		if err := json.Unmarshal(tok.ScopesJSON, &scopes); err != nil || scopes == nil {
			return nil, ports.ErrUnauthorized
		}
		return &ports.Actor{Type: ports.ActorTypeUser, ID: tok.User.ID.String(), Name: tok.User.Username, Role: tok.User.Role, Scopes: scopes}, nil
	}
	if !errors.Is(err, ports.ErrAuthenticationNotFound) {
		return nil, err
	}
	return nil, ports.ErrUnauthorized
}

// Logout returns only after storage confirms deletion or absence of the session.
func (s *Service) Logout(ctx context.Context, rawToken string) error {
	if rawToken == "" {
		return nil
	}
	sess, err := s.repo.FindSession(ctx, s.secrets.HashToken(rawToken))
	if errors.Is(err, ports.ErrAuthenticationNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	return s.repo.DeleteSession(ctx, sess.ID)
}
