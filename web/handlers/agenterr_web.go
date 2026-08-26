package handlers

import "errors"

// errEntitlementsUnwired is what the agent plugin's CanUseAgents seam gets when
// this host cannot answer the question at all.
//
// An ERROR rather than false, and the difference matters: false is a decision
// ("this member may not"), and a host with no entitlements service has not
// decided anything. The plugin treats an error as a refusal for exactly that
// reason — a seam that failed has not answered, and guessing yes opens the door
// at the moment the decider is broken — but it can say so, where a bare false
// would look like an ordinary denial and send somebody hunting a missing grant.
//
// core.New requires an EntitlementsService, so on this host it is unreachable.
// It exists because "unreachable" is a property of today's wiring, not of the
// type.
var errEntitlementsUnwired = errors.New("entitlements service is not wired")
