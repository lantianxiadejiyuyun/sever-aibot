package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestHighRiskRequiresEnabledTOTPAndLiveStepUp(t *testing.T) {
	st, a, sess, v, at := accountFixture(t)
	ctx := context.Background()
	check := func(code string, want error) {
		t.Helper()
		if err := st.ConsumeHighRiskTOTP(ctx, a.ID, sess.ID, code, v, at); !errors.Is(err, want) {
			t.Fatalf("want %v got %v", want, err)
		}
	}
	check("123456", ErrTOTPDisabled)
	seed, codes := enrollFixture(t, st, a, sess, v, at)
	at = at.Add(30 * time.Second)
	code := totpCode(seed, at.Unix()/30)
	check(code, ErrHighRiskStepUp)
	until := at.Add(time.Minute)
	if err := st.SetStepUp(ctx, sess.ID, until); err != nil {
		t.Fatal(err)
	}
	check("", ErrOTPRequired)
	check(codes[0], ErrOTPInvalid)
	check("ABCDEF", ErrOTPInvalid)
	state, err := st.AccountSecurity(ctx, a.ID)
	if err != nil || state.RecoveryCodesRemaining != 10 {
		t.Fatal("high-risk request consumed a recovery code")
	}
	if err = st.ConsumeHighRiskTOTP(ctx, a.ID, sess.ID, code, nil, at); !errors.Is(err, ErrAccountVault) {
		t.Fatal(err)
	}
	check(code, nil)
	check(code, ErrOTPInvalid)
	// Codes are globally single-use across login and high-risk authorization.
	if _, _, err = st.CreateAccountSession(ctx, a, code, "", "", time.Hour, v, at); !errors.Is(err, ErrOTPInvalid) {
		t.Fatalf("login replay %v", err)
	}
	at = at.Add(30 * time.Second)
	code = totpCode(seed, at.Unix()/30)
	expired := at.Add(-time.Second)
	if err = st.SetStepUp(ctx, sess.ID, expired); err != nil {
		t.Fatal(err)
	}
	check(code, ErrHighRiskStepUp)
	if err = st.SetStepUp(ctx, sess.ID, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// Denied requests never consume the code.
	check(code, nil)
	if _, err = st.db.Exec(`DELETE FROM admin_sessions WHERE id=?`, sess.ID); err != nil {
		t.Fatal(err)
	}
	check(code, ErrAccountChanged)
}
func TestHighRiskConcurrentTOTPOnlyOnce(t *testing.T) {
	st, a, sess, v, at := accountFixture(t)
	ctx := context.Background()
	seed, _ := enrollFixture(t, st, a, sess, v, at)
	at = at.Add(30 * time.Second)
	if err := st.SetStepUp(ctx, sess.ID, at.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	code := totpCode(seed, at.Unix()/30)
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- st.ConsumeHighRiskTOTP(ctx, a.ID, sess.ID, code, v, at) }()
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrOTPInvalid) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("successes=%d", successes)
	}
}
