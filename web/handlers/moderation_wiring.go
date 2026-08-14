package handlers

// Where the /moderation routes are registered.
//
// They used to sit inline in wireAdminAndViews, and that had a cost nobody was
// paying attention to: the whole moderation surface was unreachable from any
// test, because wireAdminAndViews takes a *core.Core, a *core.Runtime, a plugin
// inbox and a redis client, and the handler harness has none of those.
//
// The block never needed ANY of them. It uses the engine, wsrv.auth and wsrv's
// own handlers and nothing else — so it was untestable purely by where it was
// written. Moderation is also not administration: it gates at RoleMod, it is
// linked from a different place, and bundling it with /admin made it look like
// it shared /admin's dependencies.

import (
	"github.com/gin-gonic/gin"

	"github.com/the-loon-clan/loon/core"
)

// mountModeration registers the moderation surface: the community queue, the
// avatar queue, and the cheat-detection queue.
//
// Gates are per-route rather than on the group, because they differ: the
// community queue is admin, the two staff queues are RoleMod, and the group
// itself only requires being signed in.
func mountModeration(engine *gin.Engine, wsrv *web) {
	moderation := engine.Group("/moderation", wsrv.auth.Require(core.RoleUser)...)

	adminOnly := wsrv.auth.Require(core.RoleAdmin)
	moderation.GET("", append(adminOnly, wsrv.communityModPage)...)
	moderation.POST("/vote", append(adminOnly, wsrv.communityModVote)...)

	staffOnly := wsrv.auth.Require(core.RoleMod)
	moderation.GET("/avatars", append(staffOnly, wsrv.avatarModPage)...)
	moderation.POST("/avatars", append(staffOnly, wsrv.avatarModAction)...)
	// The cheat-detection queue, beside the avatar queue on purpose: a queue
	// is only worked if it is somewhere a moderator already goes. Same staff
	// gate — reading a flag is a moderation act, and the flags name members.
	moderation.GET("/cheat", append(staffOnly, wsrv.cheatQueuePage)...)
	moderation.POST("/cheat/clear", append(staffOnly, wsrv.cheatQueueClear)...)
}
