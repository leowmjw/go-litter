package tutorialserver

import (
	"context"
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"strings"
	"time"

	"github.com/starfederation/datastar-go/datastar"

	"app/internal/ramaspace"
)

const stage6Description = `Stage 6 combines the preceding concepts in RamaSpace: acknowledged
stream ETLs maintain users and friendships, microbatch ETLs materialize posts and analytics,
and query paths resolve several partitioned PStates in one request.`

func (s *Server) addStage6Routes() {
	s.mux.HandleFunc("GET /stage6", func(w http.ResponseWriter, r *http.Request) {
		writePage(w, pageData{
			Title:       "Stage 6 — RamaSpace",
			Description: stage6Description,
			SignalsJSON: signalsJSON(statusSignal{Status: "ready"}),
			Content:     stage6Content,
		})
	})

	s.handleStage6Action("POST /stage6/seed", "registered Alice and Bob", func(ctx context.Context) error {
		for _, registration := range []ramaspace.UserRegistration{
			{UserID: "alice", Email: "alice@example.test", DisplayName: "Alice", PasswordHash: "alice-hash", RegistrationUUID: "tutorial-alice"},
			{UserID: "bob", Email: "bob@example.test", DisplayName: "Bob", PasswordHash: "bob-hash", RegistrationUUID: "tutorial-bob"},
		} {
			if _, err := s.stage6.RegisterUser(ctx, registration); err != nil {
				return err
			}
		}
		if err := s.stage6.EditProfile(ctx, ramaspace.ProfileEdit{UserID: "alice", Field: ramaspace.FieldProfilePicture, Value: "alice.png"}); err != nil {
			return err
		}
		return s.stage6.EditProfile(ctx, ramaspace.ProfileEdit{UserID: "bob", Field: ramaspace.FieldProfilePicture, Value: "bob.png"})
	})
	s.handleStage6Action("POST /stage6/request", "Alice requested Bob as a friend", func(ctx context.Context) error {
		return s.stage6.FriendRequest(ctx, "alice", "bob")
	})
	s.handleStage6Action("POST /stage6/accept", "friend request accepted atomically", func(ctx context.Context) error {
		return s.stage6.AcceptFriendRequest(ctx, "alice", "bob")
	})
	s.handleStage6Action("POST /stage6/post", "post appended; advance the microbatch to materialize it", func(ctx context.Context) error {
		return s.stage6.Post(ctx, ramaspace.Post{Author: "alice", Destination: "bob", Content: "Hello from the RamaSpace tutorial"})
	})
	s.handleStage6Action("POST /stage6/view", "profile view appended; advance the microbatch to aggregate it", func(ctx context.Context) error {
		// Pin the timestamp to a deterministic hour bucket so the snapshot query
		// always matches it, avoiding an hour-boundary flake in tests and UI.
		bucket := time.Date(2024, time.January, 1, 12, 5, 0, 0, time.UTC)
		return s.stage6.RecordProfileView(ctx, ramaspace.ProfileView{UserID: "bob", TimestampMillis: bucket.UnixMilli()})
	})
	s.handleStage6Action("POST /stage6/advance", "microbatch checkpoint committed", func(ctx context.Context) error {
		_, err := s.stage6.Advance(ctx)
		return err
	})
	s.handleStage6Action("POST /stage6/refresh", "refreshed materialized state", func(context.Context) error { return nil })
}

func (s *Server) handleStage6Action(pattern, success string, action func(context.Context) error) {
	s.mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
		if err := action(r.Context()); err != nil {
			writeSSEErrorWithResult(w, r, "stage6-state", err)
			return
		}
		snapshot, err := s.stage6Snapshot(r.Context())
		if err != nil {
			writeSSEErrorWithResult(w, r, "stage6-state", err)
			return
		}
		sse := datastar.NewSSE(w, r)
		_ = sse.PatchElements(snapshot, datastar.WithSelectorID("stage6-state"), datastar.WithModeInner())
		_ = patchSignals(sse, statusSignal{Status: success})
	})
}

func (s *Server) stage6Snapshot(ctx context.Context) (string, error) {
	alice, aliceFound, err := s.stage6.GetProfile(ctx, "alice")
	if err != nil {
		return "", err
	}
	bob, bobFound, err := s.stage6.GetProfile(ctx, "bob")
	if err != nil {
		return "", err
	}
	outgoing, err := s.stage6.ListOutgoingRequests(ctx, "alice", "", ramaspace.MaxPageSize)
	if err != nil {
		return "", err
	}
	incoming, err := s.stage6.ListIncomingRequests(ctx, "bob", "", ramaspace.MaxPageSize)
	if err != nil {
		return "", err
	}
	friends, err := s.stage6.ListFriends(ctx, "alice", "", ramaspace.MaxPageSize)
	if err != nil {
		return "", err
	}
	friendCount, err := s.stage6.FriendCount(ctx, "alice")
	if err != nil {
		return "", err
	}
	postCount, err := s.stage6.PostCount(ctx, "bob")
	if err != nil {
		return "", err
	}
	posts, err := s.stage6.ResolvePosts(ctx, "bob", 0, ramaspace.MaxPageSize)
	if err != nil {
		return "", err
	}
	// Match the fixed hour bucket used when recording the profile view.
	hour := time.Date(2024, time.January, 1, 12, 0, 0, 0, time.UTC).UnixMilli() / int64(time.Hour/time.Millisecond)
	viewCount, err := s.stage6.ProfileViewCount(ctx, "bob", hour, hour+1)
	if err != nil {
		return "", err
	}

	b := &strings.Builder{}
	b.WriteString(`<div class="output-grid">`)
	b.WriteString(`<section class="result-card"><h3>Profiles (stream)</h3>`)
	if aliceFound {
		fmt.Fprintf(b, `<p><strong>%s</strong> · %s</p>`, htmlEscape(alice.DisplayName), htmlEscape(alice.ProfilePicture))
	}
	if bobFound {
		fmt.Fprintf(b, `<p><strong>%s</strong> · %s</p>`, htmlEscape(bob.DisplayName), htmlEscape(bob.ProfilePicture))
	}
	if !aliceFound && !bobFound {
		b.WriteString(`<p class="muted">Seed the users first.</p>`)
	}
	b.WriteString(`</section>`)
	fmt.Fprintf(b, `<section class="result-card"><h3>Friendships (stream)</h3><dl class="result-list"><dt>Alice outgoing</dt><dd>%s</dd><dt>Bob incoming</dt><dd>%s</dd><dt>Alice friends</dt><dd>%s</dd><dt>Friend count</dt><dd>%d</dd></dl></section>`,
		renderStringList(outgoing.UserIDs), renderStringList(incoming.UserIDs), renderStringList(friends.UserIDs), friendCount)
	fmt.Fprintf(b, `<section class="result-card"><h3>Bob's wall (microbatch)</h3><p>Count: <strong>%d</strong></p>%s</section>`, postCount, renderPostList(posts.Posts))
	fmt.Fprintf(b, `<section class="result-card"><h3>Bob's profile views (microbatch)</h3><p class="metric">%d</p><p>Current hour bucket</p></section>`, viewCount)
	b.WriteString(`</div>`)
	return b.String(), nil
}

func renderPostList(posts []ramaspace.ResolvedPost) string {
	if len(posts) == 0 {
		return `<p class="muted">No materialized posts. Append one, then advance.</p>`
	}
	b := &strings.Builder{}
	b.WriteString(`<ol>`)
	for _, post := range posts {
		fmt.Fprintf(b, `<li><strong>%s</strong>: %s</li>`, htmlEscape(post.AuthorDisplayName), htmlEscape(post.Content))
	}
	b.WriteString(`</ol>`)
	return b.String()
}

var stage6Content = template.HTML(`
<div class="card">
    <h2>Build the capstone one operation at a time</h2>
    <p>Follow the buttons from left to right. Observe that users and friendships update immediately, while posts and views wait for a microbatch checkpoint.</p>
    <button data-on:click="@post('/stage6/seed')">1. Seed Alice + Bob</button>
    <button data-on:click="@post('/stage6/request')">2. Friend Request</button>
    <button data-on:click="@post('/stage6/accept')">3. Accept</button>
    <button data-on:click="@post('/stage6/post')">4. Append Post</button>
    <button data-on:click="@post('/stage6/view')">5. Append View</button>
    <button data-on:click="@post('/stage6/advance')">6. Advance Microbatch</button>
    <button data-on:click="@post('/stage6/refresh')">Refresh</button>
</div>
<div id="stage6-state" class="card">
    <p class="muted">Seed the demo to begin.</p>
</div>
<div class="card">
    <h2>Raw RamaSpace API</h2>
    <p>The existing JSON API is mounted in this process at <code>/stage6/api/</code>, including <code>/stage6/api/users</code>, <code>/stage6/api/posts</code>, and <code>/stage6/api/admin/advance</code>.</p>
</div>
`)

// Config holds CLI options for the tutorial server.
type Config struct {
	Address string
}

// RunCLI parses flags and starts the interactive tutorial server.
func RunCLI(ctx context.Context, args []string) error {
	config, err := ParseConfig(args)
	if err != nil {
		return err
	}
	server, err := New(config.Address)
	if err != nil {
		return fmt.Errorf("new tutorial server: %w", err)
	}
	return server.ListenAndServe(ctx)
}

// ParseConfig parses the tutorial-server flags without side effects.
func ParseConfig(args []string) (Config, error) {
	flags := flag.NewFlagSet("tutorial", flag.ContinueOnError)
	config := Config{Address: defaultAddr}
	flags.StringVar(&config.Address, "addr", defaultAddr, "HTTP listen address")
	if err := flags.Parse(args); err != nil {
		return Config{}, err
	}
	return config, nil
}
