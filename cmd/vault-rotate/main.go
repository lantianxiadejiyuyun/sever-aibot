// Command vault-rotate rotates stored ciphertext offline. Stop control and take
// a database backup before running; keys are read only from the environment.
package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"server-aibot/internal/store"
	"server-aibot/internal/vault"
	"time"
)

func run() error {
	path := os.Getenv("AIBOT_DB")
	if path == "" {
		return fmt.Errorf("AIBOT_DB is required")
	}
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("existing database required: %w", err)
	}
	key := func(name string) (*vault.Vault, error) {
		b, err := base64.StdEncoding.DecodeString(os.Getenv(name))
		if err != nil {
			return nil, fmt.Errorf("%s must be base64", name)
		}
		defer clear(b)
		return vault.New(b)
	}
	old, err := key("AIBOT_MASTER_KEY")
	if err != nil {
		return err
	}
	next, err := key("AIBOT_NEW_MASTER_KEY")
	if err != nil {
		return err
	}
	if old.KeyID() == next.KeyID() {
		return fmt.Errorf("new key must differ from old key")
	}
	st, err := store.Open(path)
	if err != nil {
		return err
	}
	defer st.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	n, err := st.RotateCredentialKey(ctx, old, next)
	if err != nil {
		return err
	}
	fmt.Printf("Rotated %d encrypted records. Install AIBOT_NEW_MASTER_KEY as AIBOT_MASTER_KEY before restarting control.\n", n)
	return nil
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
