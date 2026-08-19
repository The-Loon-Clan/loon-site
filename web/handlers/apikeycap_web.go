package handlers

import (
	"context"

	"github.com/the-loon-clan/loon-baseline/apikey"
)

// The host's API key, published as a capability.
//
// A plugin that exposes an endpoint for a member's own tooling — a download
// client, a script, a cron job — needs exactly one thing out of the whole key
// system: which member is this. Publishing that one answer is what stops every
// such plugin from growing a key table of its own, and it is why the seam is
// this narrow.
//
// An adapter rather than registering the store directly, and the reason is the
// method name. loon-baseline's store calls it Resolve, which is right for a
// package about keys and wrong for a registry entry any plugin can look up —
// "resolve" alone says nothing about what is being resolved. The seam spells it
// ResolveAPIKey, and this is the three lines that reconcile them.
type apiKeyResolver struct{ store *apikey.PGStore }

// ResolveAPIKey answers pluginapi.APIKeyResolver.
//
// The distinction the seam's doc insists on is preserved here because the store
// already makes it: an unknown key is ok=false with a nil error, not an error.
// Keys arrive from the open internet, so a typo, a rotated key and a probe are
// ordinary events — a caller answers them with 401, and keeps 500 for a lookup
// that genuinely failed.
func (r apiKeyResolver) ResolveAPIKey(ctx context.Context, key string) (int64, bool, error) {
	if r.store == nil {
		return 0, false, nil
	}
	return r.store.Resolve(ctx, key)
}

// APIKeyFor answers the optional pluginapi.APIKeyIssuer half: what is THIS
// member's key.
//
// Through Ensure, which mints one for a member who has never opened the API
// page — so a preconfigured script works for somebody who did not know they
// had a key, which is most people. It is the same call the API-key page makes,
// so a key created here is the same key shown there.
func (r apiKeyResolver) APIKeyFor(ctx context.Context, userID int64) (string, error) {
	if r.store == nil {
		return "", nil
	}
	k, err := r.store.Ensure(ctx, userID)
	if err != nil {
		return "", err
	}
	return k.Key, nil
}
