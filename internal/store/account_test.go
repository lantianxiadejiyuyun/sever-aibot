package store

import (
	"context"
	"encoding/base32"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"server-aibot/internal/vault"
)

func accountFixture(t *testing.T) (*Store, *Admin, *Session, *vault.Vault, time.Time) {
	t.Helper()
	st := openTest(t)
	ctx := context.Background()
	secret := strings.Repeat("initial-", 4)
	if _, err := st.EnsureAdmin(ctx, "admin", secret); err != nil {
		t.Fatal(err)
	}
	admin, err := st.Authenticate(ctx, "admin", secret)
	if err != nil {
		t.Fatal(err)
	}
	sess, _, err := st.CreateSession(ctx, admin.ID, "", "", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	v, err := vault.New([]byte(strings.Repeat("v", 32)))
	if err != nil {
		t.Fatal(err)
	}
	return st, admin, sess, v, time.Now().UTC().Truncate(30 * time.Second).Add(5 * time.Second)
}
func enrollFixture(t *testing.T, st *Store, a *Admin, sess *Session, v *vault.Vault, at time.Time) ([]byte, []string) {
	t.Helper()
	setup, err := st.BeginTOTP(context.Background(), a, sess.ID, v, at)
	if err != nil {
		t.Fatal(err)
	}
	seed, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.Secret)
	if err != nil {
		t.Fatal(err)
	}
	codes, err := st.ConfirmTOTP(context.Background(), a, sess.ID, totpCode(seed, at.Unix()/30), v, at)
	if err != nil {
		t.Fatal(err)
	}
	if len(codes) != 10 {
		t.Fatalf("codes=%d", len(codes))
	}
	return seed, codes
}
func TestAccountTOTPReferenceVectors(t *testing.T) {
	// RFC 6238 SHA1 vectors, reduced to the six digits used by this console.
	seed := []byte("12345678901234567890")
	cases := []struct {
		unix int64
		code string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}, {20000000000, "353130"}}
	for _, c := range cases {
		if got := totpCode(seed, c.unix/30); got != c.code {
			t.Fatalf("unix %d: %s", c.unix, got)
		}
	}
}
func TestAccountFactorsAtomicAndReplayProtected(t *testing.T) {
	st, a, sess, v, at := accountFixture(t)
	ctx := context.Background()
	seed, codes := enrollFixture(t, st, a, sess, v, at)
	var sealed []byte
	var hash string
	if err := st.db.QueryRow(`SELECT totp_seed_ct FROM admin_security WHERE admin_id=?`, a.ID).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sealed), string(seed)) {
		t.Fatal("seed stored in plaintext")
	}
	if err := st.db.QueryRow(`SELECT code_hash FROM admin_recovery_codes WHERE admin_id=? LIMIT 1`, a.ID).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	for _, code := range codes {
		if hash == code {
			t.Fatal("recovery stored in plaintext")
		}
	}
	before := sessionCount(t, st, a.ID)
	for _, tc := range []struct {
		otp  string
		want error
	}{{"", ErrOTPRequired}, {"bad-code", ErrOTPInvalid}, {totpCode(seed, at.Unix()/30), ErrOTPInvalid}} {
		issued, token, err := st.CreateAccountSession(ctx, a, tc.otp, "", "", time.Hour, v, at)
		if !errors.Is(err, tc.want) || issued != nil || token != "" {
			t.Fatalf("factor result sess=%v tokenEmpty=%v err=%v", issued, token == "", err)
		}
	}
	if sessionCount(t, st, a.ID) != before {
		t.Fatal("failed factor issued a session")
	}
	at = at.Add(30 * time.Second)
	code := totpCode(seed, at.Unix()/30)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := st.CreateAccountSession(ctx, a, code, "", "", time.Hour, v, at)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrOTPInvalid) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("concurrent success=%d", success)
	}
	if _, _, err := st.CreateAccountSession(ctx, a, codes[0], "", "", time.Hour, nil, at); err != nil {
		t.Fatalf("recovery with missing vault: %v", err)
	}
	if _, _, err := st.CreateAccountSession(ctx, a, codes[0], "", "", time.Hour, v, at); !errors.Is(err, ErrOTPInvalid) {
		t.Fatalf("reused recovery: %v", err)
	}
	state, err := st.AccountSecurity(ctx, a.ID)
	if err != nil || state.RecoveryCodesRemaining != 9 {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}
func sessionCount(t *testing.T, st *Store, id string) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT COUNT(*) FROM admin_sessions WHERE admin_id=?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestAccountRotationRevocationAndRollback(t *testing.T) {
	st, a, sess, v, at := accountFixture(t)
	ctx := context.Background()
	_, codes := enrollFixture(t, st, a, sess, v, at)
	other, _, err := st.CreateAccountSession(ctx, a, codes[0], "", "", time.Hour, v, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.UpdateTOTP(ctx, a, "revoked-session", codes[1], false, v, at); !errors.Is(err, ErrAccountChanged) {
		t.Fatal(err)
	}
	// A failed mutation must not consume its recovery code.
	if err = st.ChangeAccountPassword(ctx, a, sess.ID, strings.Repeat("replacement-", 3), codes[1], v, at); err != nil {
		t.Fatal(err)
	}
	if sessionCount(t, st, a.ID) != 1 {
		t.Fatal("other sessions not revoked")
	}
	var n int
	st.db.QueryRow(`SELECT COUNT(*) FROM admin_sessions WHERE id=?`, other.ID).Scan(&n)
	if n != 0 {
		t.Fatal("other session remains")
	}
	if _, err = st.Authenticate(ctx, "admin", strings.Repeat("initial-", 4)); err == nil {
		t.Fatal("old credential accepted")
	}
	fresh, err := st.Authenticate(ctx, "admin", strings.Repeat("replacement-", 3))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.CreateAccountSession(ctx, a, codes[2], "", "", time.Hour, v, at); !errors.Is(err, ErrAccountChanged) {
		t.Fatalf("stale authentication: %v", err)
	}
	newer, err := st.UpdateTOTP(ctx, fresh, sess.ID, codes[2], false, v, at)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = st.CreateAccountSession(ctx, fresh, codes[3], "", "", time.Hour, v, at); !errors.Is(err, ErrOTPInvalid) {
		t.Fatal("old recovery survived reset")
	}
	if _, err = st.UpdateTOTP(ctx, fresh, sess.ID, newer[0], true, v, at); err != nil {
		t.Fatal(err)
	}
	state, err := st.AccountSecurity(ctx, a.ID)
	if err != nil || state.TOTPEnabled || state.RecoveryCodesRemaining != 0 {
		t.Fatalf("disable state %+v %v", state, err)
	}
	if _, _, err = st.CreateAccountSession(ctx, fresh, "", "", "", time.Hour, nil, at); err != nil {
		t.Fatal(err)
	}
}
func TestAccountRecoveryConcurrentAndTransactionRollback(t *testing.T) {
	st, a, sess, v, at := accountFixture(t)
	ctx := context.Background()
	_, codes := enrollFixture(t, st, a, sess, v, at)
	if _, err := st.db.Exec(`CREATE TRIGGER reject_account_session BEFORE INSERT ON admin_sessions BEGIN SELECT RAISE(ABORT, 'injected session failure'); END`); err != nil {
		t.Fatal(err)
	}
	issued, token, err := st.CreateAccountSession(ctx, a, codes[0], "", "", time.Hour, v, at)
	if err == nil || issued != nil || token != "" {
		t.Fatal("session failure returned authentication material")
	}
	state, err := st.AccountSecurity(ctx, a.ID)
	if err != nil || state.RecoveryCodesRemaining != 10 {
		t.Fatal("failed session creation consumed recovery code")
	}
	if _, err = st.db.Exec(`DROP TRIGGER reject_account_session`); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := st.CreateAccountSession(ctx, a, codes[0], "", "", time.Hour, v, at)
			results <- err
		}()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		} else if !errors.Is(err, ErrOTPInvalid) {
			t.Fatal(err)
		}
	}
	if success != 1 {
		t.Fatalf("concurrent recovery successes=%d", success)
	}
	state, err = st.AccountSecurity(ctx, a.ID)
	if err != nil || state.RecoveryCodesRemaining != 9 {
		t.Fatalf("recovery count %+v %v", state, err)
	}
}

func TestAccountEnrollmentExpiryAndBadConfirmation(t *testing.T) {
	st, a, sess, v, at := accountFixture(t)
	ctx := context.Background()
	setup, err := st.BeginTOTP(ctx, a, sess.ID, v, at)
	if err != nil {
		t.Fatal(err)
	}
	seed, _ := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(setup.Secret)
	if _, err = st.ConfirmTOTP(ctx, a, sess.ID, "bad", v, at); !errors.Is(err, ErrOTPInvalid) {
		t.Fatal(err)
	}
	state, _ := st.AccountSecurity(ctx, a.ID)
	if state.TOTPEnabled {
		t.Fatal("bad code enabled TOTP")
	}
	if _, err = st.ConfirmTOTP(ctx, a, sess.ID, totpCode(seed, at.Add(11*time.Minute).Unix()/30), v, at.Add(11*time.Minute)); !errors.Is(err, ErrTOTPSetupExpired) {
		t.Fatal(err)
	}
}
