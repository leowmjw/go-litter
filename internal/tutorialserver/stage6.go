package tutorialserver

import (
	"context"
	"flag"
	"fmt"
	"html/template"
	"net/http"
)

const stage6Description = `Stage 6 is the RamaSpace capstone: a full social-network application
that combines all previous concepts. It is exposed through the main <code>quick-tutorial</code>
command rather than this teaching server, because it needs Pebble persistence, Temporal
lifecycle, and a long-running microbatch scheduler.`

func (s *Server) addStage6Placeholder() {
	s.mux.HandleFunc("GET /stage6", func(w http.ResponseWriter, r *http.Request) {
		writePage(w, pageData{
			Title:       "Stage 6 — RamaSpace",
			Description: stage6Description,
			SignalsJSON: signalsJSON(statusSignal{Status: "ready"}),
			Content: template.HTML(`
<div class="card">
    <h2>Launch the capstone</h2>
    <p>Run the standalone RamaSpace server:</p>
    <pre>mise run quick-tutorial:run</pre>
    <p>Or directly with Go:</p>
    <pre>go run ./cmd/quick-tutorial</pre>
    <p>Then explore the API endpoints for users, profiles, friendships, posts, and analytics.</p>
</div>
`),
		})
	})
}

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
