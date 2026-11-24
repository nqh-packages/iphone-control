package main

import (
	"context"
	"log/slog"

	"github.com/DIYgod/pinky/internal/api"
	"github.com/DIYgod/pinky/internal/policy"
	"github.com/DIYgod/pinky/internal/vault"
)

// newSecretVault starts the vault for secrets the owner's credential broker
// stages (`pinkyctl phone secret put`): in memory, for the apps on the
// policy's credential_apps only. Its socket lives in the gateway's private
// runtime folder. When the socket cannot be made the gateway runs without a
// vault, so every {{secret:NAME}} is refused. stop ends the socket.
func newSecretVault(pol *policy.Engine, dirs api.Dirs, log *slog.Logger) (v *vault.Vault, stop func()) {
	v = vault.New(pol.SecretApp)
	ln, err := vault.Listen(vaultSocketIn(dirs))
	if err != nil {
		log.Warn("secrets cannot be staged: the vault socket did not start", "err", err)
		return nil, func() {}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := v.Serve(ctx, ln); err != nil {
			log.Warn("the vault socket stopped", "err", err)
		}
	}()
	return v, func() { cancel(); <-done }
}
