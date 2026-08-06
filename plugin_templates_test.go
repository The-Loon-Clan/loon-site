package main

import (
	"bytes"
	"html/template"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/the-loon-clan/loon-plugins/communities"
	"github.com/the-loon-clan/loon-plugins/donations"
	"github.com/the-loon-clan/loon-plugins/messages"
	"github.com/the-loon-clan/loon-plugins/news"
	"github.com/the-loon-clan/loon-plugins/playlists"
	"github.com/the-loon-clan/loon-plugins/wiki"
)

// Every template under web/templates/plugin/ is rendered by a PLUGIN through
// gin's HTML set, which means nothing in this repo calls it and no build, vet
// or parse check exercises it. The failure mode is execute-time and silent:
// the handler logs, and the browser gets a truncated page.
//
// Three real bugs of exactly that shape were caught by hand while these
// templates were being written:
//
//	{{int64 .X}}                  — not a template function
//	{{.CSRFToken}} inside a range — resolves to nothing, every delete 403s
//	{{.Theme.Href}} with no Theme — aborts mid-document
//
// So this sweeps every one of them twice: once with ONLY the chrome keys plus
// whatever the handler structurally guarantees (the shape a fresh install
// renders), and once populated (the shape with rows in it). Between them they
// exercise both branches of every {{if}} guarding a list.
//
// Uses the plugins' REAL row types rather than maps on purpose: a map lookup
// for a missing key yields nil, so a field-name typo would pass. A struct field
// that does not exist is a compile error, which is the point.

// pluginFixture is one template plus the two data shapes to render it with.
type pluginFixture struct {
	page string
	// structural is what the handler ALWAYS sets. A nil list counts; an absent
	// key does not. Merged over chromeKeys().
	structural map[string]any
	// populated adds rows. Merged over structural. nil = no second pass.
	populated map[string]any
}

func pluginFixtures() []pluginFixture {
	now := time.Now()
	page := hostPagination(1, 25, 1, "/x")
	comm := &communities.Community{
		ID: 1, Slug: "usenet", Name: "Usenet", JoinType: "open",
		SubscriberCount: 3, CreatedAt: now, UpdatedAt: now,
	}

	// news.Handlers builds an anonymous projection rather than passing its
	// model, so the fixture mirrors that shape instead of using NewsPost.
	newsPost := struct {
		ID        int64
		Title     string
		Slug      string
		Body      template.HTML
		CreatedAt any
	}{1, "Hello", "hello", template.HTML("<p>b</p>"), now}

	return []pluginFixture{
		// ── news
		{"news.html",
			map[string]any{"News": nil},
			map[string]any{"News": []any{newsPost}}},
		{"news_detail.html",
			map[string]any{"Post": newsPost}, nil},
		{"admin_news.html",
			map[string]any{"Posts": []news.NewsPost(nil)},
			map[string]any{"Posts": []news.NewsPost{{ID: 1, Title: "t", Slug: "s", Published: true, CreatedAt: now, UpdatedAt: now}}}},
		{"admin_news_form.html",
			map[string]any{"Post": news.NewsPost{ID: 1, Title: "t", Slug: "s", CreatedAt: now, UpdatedAt: now}}, nil},

		// ── wiki
		{"wiki.html",
			map[string]any{"Topics": nil, "RecentPosts": nil, "PopularPosts": nil},
			map[string]any{
				"Topics":       []*wiki.Topic{{ID: 1, Name: "Guides", Slug: "guides", Icon: "book", PostCount: 2}},
				"RecentPosts":  []*wiki.RecentPost{{ID: 1, Title: "P", Slug: "p", TopicName: "Guides", TopicSlug: "guides", UpdatedAt: now}},
				"PopularPosts": []*wiki.RecentPost{{ID: 1, Title: "P", Slug: "p", TopicName: "Guides", TopicSlug: "guides", UpdatedAt: now, ViewCount: 9}},
			}},
		{"wiki_topic.html",
			map[string]any{"Topic": &wiki.Topic{ID: 1, Name: "Guides", Slug: "guides"}, "Posts": nil, "AllTopics": nil},
			map[string]any{
				"Posts":     []*wiki.Post{{ID: 1, TopicID: 1, Title: "P", Slug: "p", UpdatedAt: now}},
				"AllTopics": []*wiki.Topic{{ID: 1, Name: "Guides", Slug: "guides"}},
			}},
		{"wiki_post.html",
			map[string]any{
				"Topic": &wiki.Topic{ID: 1, Name: "Guides", Slug: "guides"},
				"Post":  &wiki.Post{ID: 1, TopicID: 1, Title: "P", Slug: "p", UpdatedAt: now},
				// Already safe: the plugin ran it through Deps.Markdown.
				"RenderedContent": template.HTML("<p>x</p>"),
				"Posts":           nil,
			},
			map[string]any{"Posts": []*wiki.Post{{ID: 2, TopicID: 1, Title: "Q", Slug: "q"}}}},
		{"admin_wiki.html",
			map[string]any{"Topics": nil, "PostsByTopic": map[int][]*wiki.Post{}},
			map[string]any{
				"Topics":       []*wiki.Topic{{ID: 1, Name: "Guides", Slug: "guides"}},
				"PostsByTopic": map[int][]*wiki.Post{1: {{ID: 1, Title: "P"}}},
			}},
		{"admin_wiki_topic_form.html",
			map[string]any{"Action": "Create", "Icons": wiki.TopicIcons},
			map[string]any{"Action": "Edit", "Topic": &wiki.Topic{ID: 1, Name: "G", Slug: "g", Icon: "book", Color: "#4c8dff"}}},
		{"admin_wiki_post_form.html",
			map[string]any{"Action": "Create", "TopicID": 1},
			map[string]any{"Action": "Edit", "Post": &wiki.Post{ID: 1, TopicID: 1, Title: "P", Content: "# x"}}},

		// ── messages
		{"inbox.html",
			map[string]any{"Items": nil, "ActiveThreadID": int64(0), "CanSendDM": false},
			map[string]any{
				"Items":          []messages.InboxItem{{Kind: "dm", ID: 1, DisplayName: "bob", Subtitle: "hi", UpdatedAt: now, UnreadCount: 1}},
				"ActiveThreadID": int64(1),
				"ActiveCpName":   "bob",
				"ActiveCpID":     2,
				"ActiveMessages": []messages.DMMessageView{{ID: 1, ThreadID: 1, SenderID: 1, SenderUsername: "alice", Body: "hi", CreatedAt: now}},
				"CanSendDM":      true,
			}},
		{"admin_messages.html",
			map[string]any{"Messages": nil, "Users": nil, "Total": 0, "Pagination": page},
			map[string]any{
				"Messages": []messages.Announcement{{ID: 1, Title: "T", Body: "B", Target: "all", CreatedAt: now}},
				"Users":    []messages.UserOption{{ID: 1, Username: "alice"}},
				"Total":    1,
			}},

		// ── store: NOT here. The store plugin now embeds and parses its own
		// templates (store/views.go pageTmpl) and asks the host only to wrap
		// the finished fragment (Deps.RenderPage), so the host's copies were
		// dead markup and are gone. Its pages are covered by exercising them
		// against the running site, not by this sweep, which only sees
		// templates the HOST renders.

		// ── tickets: NOT here, for the same reason as store. The plugin
		// embeds and parses its own four templates and asks the host only for
		// chrome (RenderPage), the shared editor (RenderEditor), the pager and
		// Markdown. The host's copies were dead markup and are gone.

		// ── donations
		{"help_donate.html",
			map[string]any{"Groups": nil, "AddressesHidden": false, "TotalMonthlyUSD": 0.0},
			map[string]any{
				"Groups": []*donations.DonationGoalGroup{{
					Name: "site", Locks: true, MonthlyGoalUSD: 100, MonthlyRaisedUSD: 25,
					Items: []*donations.SiteCost{{ID: 1, Label: "Box", Category: "server", Period: "monthly", AmountUSD: 42, Active: true}},
				}},
				"TotalMonthlyUSD": 42.0,
				"BTCAddress":      "bc1xyz",
			}},
		{"admin_donate.html",
			map[string]any{"DonateEnabled": false, "Costs": nil, "Donations": nil, "Usernames": map[int]string{}},
			map[string]any{
				"DonateEnabled": true,
				"Costs":         []*donations.SiteCost{{ID: 1, Label: "Box", Category: "server", GoalGroup: "site", Period: "monthly", AmountUSD: 42, Active: true}},
				"Donations":     []*donations.Donation{{ID: 1, AmountUSD: 10, ReceivedAt: now}},
				"Usernames":     map[int]string{1: "alice"},
			}},

		// ── communities
		//
		// postView is unexported in the plugin (handlers.go), so the fixture
		// mirrors its shape: the model plus the already-rendered body. That is
		// deliberate — BodyHTML is what the template must render, and
		// .Body is the untrusted source it must never touch.
		{"communities_index.html",
			map[string]any{"Communities": nil, "Total": 0, "Pagination": page},
			map[string]any{
				"Communities": []*communities.Community{{
					ID: 1, Slug: "usenet", Name: "Usenet", Description: "d",
					JoinType: "open", SubscriberCount: 3, ThreadCount: 2,
				}},
				"Total": 1,
			}},
		{"community_new.html",
			map[string]any{},
			map[string]any{"Error": "bad slug", "Slug": "x", "Name": "n", "Description": "d"}},
		{"community_view.html",
			map[string]any{
				"Community":  comm,
				"Threads":    nil,
				"Total":      0,
				"Pagination": page,
				"Rules":      nil,
				"Mods":       nil,
				"Role":       &communities.CommunityViewerRole{},
			},
			map[string]any{
				"Threads": []*communities.CommunityThread{{
					ID: 1, CommunityID: 1, Title: "T", Username: "bob",
					CommunitySlug: "usenet", ReplyCount: 2, LastPostAt: now, CreatedAt: now,
				}},
				"Rules":           []*communities.CommunityRule{{ID: 1, Title: "Be civil", Body: "b"}},
				"Mods":            []*communities.CommunityMod{{UserID: 1, Username: "alice", Role: "admin"}},
				"Role":            &communities.CommunityViewerRole{IsOwner: true, IsMod: true, IsSubscriber: true},
				"SidebarHTML":     template.HTML("<p>side</p>"),
				"DescriptionHTML": template.HTML("<p>desc</p>"),
				"PendingCount":    2,
				"Flash":           "saved",
			}},
		{"community_new_thread_c.html",
			map[string]any{"Community": comm}, nil},
		{"community_thread_c.html",
			map[string]any{
				"Community": comm,
				"Thread": &communities.CommunityThread{
					ID: 1, CommunityID: 1, Title: "T", Username: "bob",
					CommunitySlug: "usenet", CreatedAt: now, LastPostAt: now,
				},
				"BodyHTML":   template.HTML("<p>body</p>"),
				"Posts":      nil,
				"Total":      0,
				"Pagination": page,
				"Rules":      nil,
				"Mods":       nil,
				"Role":       &communities.CommunityViewerRole{},
			},
			map[string]any{
				"Posts": []struct {
					*communities.CommunityPost
					BodyHTML template.HTML
				}{{
					CommunityPost: &communities.CommunityPost{ID: 1, ThreadID: 1, Username: "alice", CreatedAt: now},
					BodyHTML:      template.HTML("<p>reply</p>"),
				}},
				"Role": &communities.CommunityViewerRole{IsMod: true, IsSubscriber: true},
			}},
		{"community_join_requests.html",
			map[string]any{"Community": comm, "Requests": nil, "Invites": nil},
			map[string]any{
				"Requests": []*communities.CommunityJoinRequest{{
					ID: 1, CommunityID: 1, Username: "bob", Message: "please",
					PointsHeld: 5, CreatedAt: now,
				}},
				"Invites": []*communities.CommunityInvite{{
					ID: 1, CommunityID: 1, Code: "abc", MaxUses: 1, CreatedAt: now,
				}},
			}},
		{"community_settings.html",
			map[string]any{"Community": comm}, nil},

		// ── playlists
		{"playlists_index.html",
			map[string]any{"Playlists": nil, "Total": 0, "Pagination": page},
			map[string]any{
				"Playlists": []*playlists.Playlist{{
					ID: 1, Slug: "best", Name: "Best of", Description: "d",
					Public: true, Username: "alice", ItemCount: 2, UpdatedAt: now,
				}},
				"Total": 1,
			}},
		{"playlist_view.html",
			map[string]any{
				"Playlist": &playlists.Playlist{ID: 1, Slug: "best", Name: "Best of", Public: true, UpdatedAt: now},
				"Items":    nil, "IsOwner": false,
			},
			map[string]any{
				// Two items on purpose: one resolved and one whose Release is
				// nil, which is the aged-out case the template must still draw.
				"Items": []*playlists.Item{
					{ID: 1, PlaylistID: 1, ReleaseID: 10, AddedAt: now,
						Release: &playlists.Release{ID: 10, Title: "T", Size: "1 GB", Category: "TV"}},
					{ID: 2, PlaylistID: 1, ReleaseID: 11, AddedAt: now, Release: nil},
				},
				"IsOwner": true,
			}},
		{"playlist_form.html",
			map[string]any{"Action": "Create"},
			map[string]any{
				"Action":   "Save",
				"Playlist": &playlists.Playlist{ID: 1, Slug: "best", Name: "Best of"},
				"Name":     "Best of", "Description": "d", "CoverURL": "", "Public": true,
			}},

		// ── shared error page
		{"error.html",
			map[string]any{},
			map[string]any{"Code": 503, "Title": "T", "Message": "M"}},
	}
}

// TestPluginTemplatesExecute renders every plugin template in both its empty
// and populated shape. This is the check that would have caught the three
// execute-time bugs described above, and the one that keeps the next plugin
// wiring honest.
func TestPluginTemplatesExecute(t *testing.T) {
	tmpl, err := pluginTemplates()
	if err != nil {
		t.Fatalf("pluginTemplates: %v", err)
	}
	for _, f := range pluginFixtures() {
		if tmpl.Lookup(f.page) == nil {
			t.Errorf("%s: not in the plugin set", f.page)
			continue
		}
		shapes := []string{"empty"}
		if f.populated != nil {
			shapes = append(shapes, "populated")
		}
		for _, shape := range shapes {
			data := chromeKeys()
			for k, v := range f.structural {
				data[k] = v
			}
			if shape == "populated" {
				for k, v := range f.populated {
					data[k] = v
				}
			}
			var buf bytes.Buffer
			if err := tmpl.ExecuteTemplate(&buf, f.page, data); err != nil {
				t.Errorf("%s [%s]: execute: %v", f.page, shape, err)
				continue
			}
			// An execute error aborts mid-write, so the bytes already flushed
			// stay flushed. Asserting the document CLOSED is what distinguishes
			// a complete page from a truncated one.
			if !strings.Contains(buf.String(), "</html>") {
				t.Errorf("%s [%s]: no </html> — render aborted mid-document", f.page, shape)
			}
		}
	}
}

// TestEveryPluginTemplateHasAFixture keeps the sweep honest: adding a template
// without a fixture would otherwise leave it untested, which is exactly how the
// 20-of-24 gap this file closes came about.
func TestEveryPluginTemplateHasAFixture(t *testing.T) {
	// Files holding only {{define}} blocks are never executed by name, so they
	// are legitimately fixture-free.
	partialsOnly := map[string]bool{
		"wiki_shared.html": true, "tickets_shared.html": true,
		"communities_shared.html": true,
	}
	covered := map[string]bool{}
	for _, f := range pluginFixtures() {
		covered[f.page] = true
	}
	ents, err := fs.ReadDir(siteFS, "web/templates/plugin")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".html") || partialsOnly[n] {
			continue
		}
		if !covered[n] {
			t.Errorf("web/templates/plugin/%s has no fixture in pluginFixtures()", n)
		}
	}
}
