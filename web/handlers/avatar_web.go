package handlers

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"strings"

	_ "image/gif" // decoders, registered for their side effects
	_ "image/png" //

	_ "golang.org/x/image/webp"

	"golang.org/x/image/draw"

	"github.com/gin-gonic/gin"
	"github.com/jmoiron/sqlx"

	"github.com/the-loon-clan/loon/blob"
)

// Avatars — filling in the column the whole stack already reads.
//
// users.avatar_path has existed since messages_web.go added it, user_display
// exposes it as part of the plugin-facing identity contract, and the plugins
// select it: the forum, communities, messages and chat all pull it, and chat
// goes as far as building BaseURL + AvatarPath for the Discord webhook's
// avatar override. Nothing ever WROTE it. So every one of those reads returned
// "", every surface fell through to the initials tile, and chat has been
// sending Discord a URL that is just the site origin.
//
// That is the same failure docs/BACKLOG.md #1 describes: a contract with an
// unfilled half presents as "nothing has happened yet" rather than as broken.
// This is the host's half.
//
// The HOST owns it, for the same reason it owns the access modes: users is the
// host's table and user_display is the host's contract. A plugin that wrote
// avatar_path would be reaching into the one table every other plugin agreed
// to read through a view.
//
// Storage reuses what the wiki and communities already use — blob.Store over
// uploadRoot/uploadURL, blob.SniffImage for the type check — so this adds a
// namespace under an existing mount rather than a second upload system.

const (
	// avatarMaxUpload caps the RAW upload. Generous, because the output is
	// re-encoded to a couple of tens of KB regardless; this is an anti-OOM
	// ceiling, not a quality setting.
	avatarMaxUpload = 8 << 20 // 8 MB

	// avatarMaxInputPixels guards the decode. A 40 KB file can honestly claim
	// 30000x30000, and image.Decode will try to allocate W*H*4 bytes for it —
	// so the dimensions are checked from the HEADER, before the full decode.
	avatarMaxInputPixels = 4096 * 4096

	// avatarSize is the stored edge length, in pixels. The largest place an
	// avatar renders is .avatar--lg at 64px, so 256 still looks right on a 4x
	// display and everything smaller is downscaled by the browser.
	avatarSize = 256

	// avatarJPEGQuality — 85 rather than the communities pipeline's 82,
	// because faces at 256px show ringing that a 1600px banner hides.
	avatarJPEGQuality = 85
)

// avatarFiles is the blob store for the avatars/ namespace. Same root and URL
// prefix as the wiki and community uploads, so one engine.Static mount serves
// all three and there is no second path to keep in sync.
func avatarFiles() blob.Store { return blob.NewLocal(uploadRoot, uploadURL) }

// avatarName returns the store-relative name for a new avatar.
//
// NOT the uploaded filename, which is attacker-controlled and would let
// somebody choose where in the namespace their file lands. The user id makes
// an orphaned file traceable to its owner; the random half is what makes a
// REPLACEMENT land on a new URL, so a browser that cached the old avatar shows
// the new one without a cache-busting query string.
func avatarName(userID int64) (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return fmt.Sprintf("avatars/%d-%s.jpg", userID, hex.EncodeToString(b)), nil
}

// avatarBlobName turns a stored public URL back into the store-relative name
// blob.Remove wants. Returns "" for anything not under this site's upload
// prefix, which is what stops a crafted avatar_path from deleting a file
// outside the namespace.
func avatarBlobName(publicURL string) string {
	prefix := strings.TrimSuffix(uploadURL, "/") + "/"
	if !strings.HasPrefix(publicURL, prefix) {
		return ""
	}
	name := strings.TrimPrefix(publicURL, prefix)
	// No traversal, no absolute paths — blob.Local joins this onto its root.
	if name == "" || strings.Contains(name, "..") || strings.HasPrefix(name, "/") {
		return ""
	}
	return name
}

// squareCrop takes the largest centred square from img.
//
// Cropped rather than squashed. Every surface renders avatars in a square or a
// circle, so a 16:9 upload has to lose something either way; taking it off the
// sides keeps a face the right shape, and stretching does not. Centred because
// the alternative is a face detector.
func squareCrop(img image.Image) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == h {
		return img
	}
	side := w
	if h < side {
		side = h
	}
	x0 := b.Min.X + (w-side)/2
	y0 := b.Min.Y + (h-side)/2
	type subImager interface {
		SubImage(r image.Rectangle) image.Image
	}
	rect := image.Rect(x0, y0, x0+side, y0+side)
	if si, ok := img.(subImager); ok {
		return si.SubImage(rect)
	}
	// Not every decoder returns a SubImage-capable type; copy in that case.
	dst := image.NewRGBA(image.Rect(0, 0, side, side))
	draw.Draw(dst, dst.Bounds(), img, rect.Min, draw.Src)
	return dst
}

// scaleSquare resizes a square image to avatarSize. Never upscales: a 64px
// upload stays 64px rather than being blown up into a blurry 256.
func scaleSquare(img image.Image) image.Image {
	b := img.Bounds()
	if b.Dx() <= avatarSize {
		return img
	}
	dst := image.NewRGBA(image.Rect(0, 0, avatarSize, avatarSize))
	draw.CatmullRom.Scale(dst, dst.Bounds(), img, b, draw.Over, nil)
	return dst
}

// processAvatar validates and normalises one uploaded image, returning the
// JPEG bytes to store.
//
// ALWAYS re-encoded, never passed through, even when the upload is already a
// small JPEG. Re-encoding is what drops the EXIF block — phone photos carry
// GPS coordinates, and an avatar is the one image on the site a member is
// invited to upload a picture of themselves as. It also flattens anything
// clever in the container: a file that is a valid GIF and a valid script
// depending on who parses it does not survive being decoded to pixels and
// written back out.
//
// Errors are written to be shown to the person who uploaded the file.
func processAvatar(raw []byte) ([]byte, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("that file was empty")
	}
	if _, _, err := blob.SniffImage(raw); err != nil {
		// Sniffed from the bytes, never from the filename — see blob.SniffImage.
		return nil, fmt.Errorf("that is not an image the site can read (JPEG, PNG, GIF or WebP)")
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("that image could not be read")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 ||
		int64(cfg.Width)*int64(cfg.Height) > int64(avatarMaxInputPixels) {
		return nil, fmt.Errorf("that image is too large to process (%dx%d)", cfg.Width, cfg.Height)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("that image could not be read")
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scaleSquare(squareCrop(img)), &jpeg.Options{Quality: avatarJPEGQuality}); err != nil {
		return nil, fmt.Errorf("that image could not be converted")
	}
	return buf.Bytes(), nil
}

// readAvatarUpload pulls the file out of the multipart form.
//
// Returns (nil, nil) when the field is absent or empty — submitting the form
// without choosing a file is the ordinary case, not an error, and must leave
// the current avatar alone.
func readAvatarUpload(c *gin.Context, field string) ([]byte, error) {
	fh, err := c.FormFile(field)
	if err != nil || fh == nil || fh.Size == 0 {
		return nil, nil
	}
	if fh.Size > avatarMaxUpload {
		return nil, fmt.Errorf("that file is too large (max %d MB)", avatarMaxUpload>>20)
	}
	src, err := fh.Open()
	if err != nil {
		return nil, fmt.Errorf("that file could not be read")
	}
	defer src.Close() //nolint:errcheck // read-only
	// LimitReader as well as the Size check: Size comes from the request and a
	// handcrafted multipart body can understate it.
	raw, err := io.ReadAll(io.LimitReader(src, avatarMaxUpload+1))
	if err != nil {
		return nil, fmt.Errorf("that file could not be read")
	}
	if int64(len(raw)) > avatarMaxUpload {
		return nil, fmt.Errorf("that file is too large (max %d MB)", avatarMaxUpload>>20)
	}
	return raw, nil
}

// readAvatarPath returns a member's current avatar URL, or "".
func readAvatarPath(ctx context.Context, db *sqlx.DB, userID int64) string {
	if db == nil || userID <= 0 {
		return ""
	}
	var p string
	if err := db.GetContext(ctx, &p,
		`SELECT COALESCE(avatar_path, '') FROM users WHERE id = $1`, userID); err != nil {
		return ""
	}
	return p
}

// setAvatar stores a processed avatar and points the user row at it, removing
// whatever it replaced.
//
// The old file is deleted AFTER the row is updated, not before: if the delete
// fails the site is left with an orphaned file, which costs disk. In the other
// order a failed update leaves the row pointing at a file that is gone, which
// costs every page that member appears on a broken image.
func setAvatar(ctx context.Context, db *sqlx.DB, userID int64, raw []byte) error {
	data, err := processAvatar(raw)
	if err != nil {
		return err
	}
	name, err := avatarName(userID)
	if err != nil {
		return fmt.Errorf("could not store that image")
	}
	files := avatarFiles()
	url, err := files.Save(ctx, name, data)
	if err != nil {
		return fmt.Errorf("could not store that image")
	}
	old := readAvatarPath(ctx, db, userID)
	// avatar_updated_at is what puts this member back in the review queue
	// (avatarmod_web.go). Stamped in the SAME statement as the path, because a
	// picture that changed without the timestamp moving is one no moderator is
	// ever shown.
	if _, err := db.ExecContext(ctx,
		`UPDATE users SET avatar_path = $1, avatar_updated_at = now() WHERE id = $2`, url, userID); err != nil {
		// Undo the file we just wrote rather than leaving it orphaned.
		_ = files.Remove(ctx, name)
		return fmt.Errorf("could not save your avatar")
	}
	// The replaced file is NOT deleted here. Nothing deletes an avatar file
	// directly any more -- avatarsweep_web.go owns removal, and only for files
	// no row references and no undo record still needs. Undo has to be able to
	// put a picture back, and a file deleted inline cannot be.
	_ = old
	return nil
}

// clearAvatar removes a member's avatar and the file behind it.
func clearAvatar(ctx context.Context, db *sqlx.DB, userID int64) (string, error) {
	old := readAvatarPath(ctx, db, userID)
	// Clearing leaves avatar_updated_at alone: there is no picture to review,
	// and the queue predicate already requires a non-empty avatar_path.
	if _, err := db.ExecContext(ctx,
		`UPDATE users SET avatar_path = '' WHERE id = $1`, userID); err != nil {
		return "", fmt.Errorf("could not remove your avatar")
	}
	if old == "" {
		return "", nil
	}
	// The FILE stays. It is what undo restores, and avatarsweep_web.go collects
	// it once the undo window has passed.
	return recordUndo(ctx, userID, undoKindAvatar, map[string]string{"path": old}), nil
}

// undoKindAvatar is the kind recorded when an avatar is cleared.
const undoKindAvatar = "avatar.cleared"

func init() {
	// Registered beside the thing it reverses, so the two cannot drift.
	registerUndo(undoKindAvatar, func(ctx context.Context, userID int64, payload []byte) error {
		var p struct {
			Path string `json:"path"`
		}
		if err := json.Unmarshal(payload, &p); err != nil || p.Path == "" {
			return errUndoGone
		}
		// Refuse rather than restore a row pointing at a file that is gone:
		// that trades a missing avatar for a BROKEN IMAGE, which is worse
		// because it reads as a fault rather than as a choice.
		if name := avatarBlobName(p.Path); name == "" {
			return errUndoGone
		}
		if _, err := usersDB.ExecContext(ctx,
			`UPDATE users SET avatar_path = $1, avatar_updated_at = now() WHERE id = $2`,
			p.Path, userID); err != nil {
			return fmt.Errorf("could not restore your avatar")
		}
		return nil
	})
}

// settingsAvatarSave serves POST /settings/avatar — upload or remove.
//
// A separate form from the bio on the same page, deliberately. One form would
// mean every bio save re-posts the file, and the "remove" button would have to
// be distinguished from "save my text" by a hidden field.
func (w *web) settingsAvatarSave(c *gin.Context) {
	u, ok := w.currentUser(c)
	if !ok || u == nil {
		c.Redirect(http.StatusFound, "/login")
		return
	}
	ctx := c.Request.Context()

	if c.PostForm("remove") != "" {
		token, err := clearAvatar(ctx, usersDB, u.ID)
		if err != nil {
			w.log.Error("clear avatar", "user", u.ID, "err", err)
			c.Redirect(http.StatusFound, "/settings/profile?averr="+url.QueryEscape(err.Error()))
			return
		}
		// The token rides in the query so the page can offer Undo. Empty when
		// there was nothing to clear, and the template renders no offer then.
		c.Redirect(http.StatusFound, "/settings/profile?avatar=removed&undo="+url.QueryEscape(token))
		return
	}

	raw, err := readAvatarUpload(c, "avatar")
	if err != nil {
		c.Redirect(http.StatusFound, "/settings/profile?averr="+url.QueryEscape(err.Error()))
		return
	}
	if raw == nil {
		// Submitted with no file chosen. Say so rather than reporting success
		// for having done nothing.
		c.Redirect(http.StatusFound, "/settings/profile?averr="+url.QueryEscape("choose an image first"))
		return
	}
	if err := setAvatar(ctx, usersDB, u.ID, raw); err != nil {
		// The message is the one processAvatar wrote for the uploader; the
		// detail goes to the log.
		w.log.Info("avatar rejected", "user", u.ID, "reason", err)
		c.Redirect(http.StatusFound, "/settings/profile?averr="+url.QueryEscape(err.Error()))
		return
	}
	c.Redirect(http.StatusFound, "/settings/profile?avatar=saved")
}

// migrateUserDisplay replaces the baseline's user_display with one that reads
// the real columns.
//
// This is the seam, not a workaround. loon-baseline builds the view with
//
//	''::text     AS avatar_path,
//	0::smallint  AS reputation_tier
//
// and says why in a comment above it: "avatar is empty and reputation zero
// until the corresponding facet packages land — at which point only this view
// changes, no plugin." The facets have landed on this host — messages added
// users.avatar_path, the points work added users.reputation_tier — and nothing
// ever changed the view. So both columns were real, populated by the host, and
// discarded on the way out to every plugin that reads the contract.
//
// It cost more than avatars. The communities plugin joins user_display for
// exactly these fields, so its member lists have been rendering an empty
// avatar and tier 0 for every member since the day they were wired, with
// nothing anywhere reporting a problem — the fourth instance of the pattern in
// docs/BACKLOG.md #1, and the one that hid the longest, because the fallback
// (initials, tier 0) is what a real new account looks like.
//
// MUST run after the columns exist and after userStore.Migrate has created the
// baseline version, hence its call site late in main. CREATE OR REPLACE keeps
// the column names, types and order identical — Postgres rejects a replacement
// that changes them, which is a useful guard: if the baseline's shape ever
// moves, this fails loudly at boot instead of quietly serving a stale view.
func migrateUserDisplay(db *sqlx.DB) error {
	// Belt and braces: both columns are added elsewhere (messages and points),
	// and adding them here as well means this file does not silently depend on
	// which plugins a host happens to wire.
	for _, q := range []string{
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS avatar_path TEXT`,
		`ALTER TABLE users ADD COLUMN IF NOT EXISTS reputation_tier INTEGER NOT NULL DEFAULT 0`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	_, err := db.Exec(`CREATE OR REPLACE VIEW user_display AS
		SELECT id,
		       username,
		       CASE role
		           WHEN -2 THEN 'banned'
		           WHEN -1 THEN 'disabled'
		           WHEN  1 THEN 'contributor'
		           WHEN  2 THEN 'mod'
		           WHEN  3 THEN 'admin'
		           ELSE 'user'
		       END AS role,
		       COALESCE(avatar_path, '')::text        AS avatar_path,
		       COALESCE(reputation_tier, 0)::smallint AS reputation_tier
		FROM users`)
	return err
}
