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

// mountWidgetsAdmin registers the widget-layout editor.
//
// Extracted for the same reason as mountModeration: the routes were inline in
// wireAdminAndViews, so nothing could reach them without a plugin runtime. The
// difference is that these DO use the runtime — placedWidgets resolves each
// placement against the live widget registry — so extraction alone was not
// enough. placedWidgets now treats a nil registry as "nothing resolves", which
// is both true and the only sane rendering, and that is what lets the handler
// harness exercise these paths without booting a plugin runtime.
//
// Takes the admin group rather than the engine, because the gate belongs to
// the caller: this is administration, unlike moderation.
func mountWidgetsAdmin(admin *gin.RouterGroup, wsrv *web) {
	admin.GET("/widgets", wsrv.widgetsAdminPage)
	admin.POST("/widgets/apply", wsrv.widgetsAdminAction)
	// Which pages a rule reaches, answered while it is being typed and before
	// it is saved (widgetpreview.go). Separate from /apply because it must NOT
	// write: the value being asked about is the one in the box, which the
	// operator has not committed to yet.
	admin.POST("/widgets/preview", wsrv.widgetsAdminPreview)
}
