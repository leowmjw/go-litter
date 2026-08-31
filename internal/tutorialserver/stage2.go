package tutorialserver

import (
	"fmt"
	"html/template"
	"net/http"
	"strconv"
	"strings"

	"github.com/starfederation/datastar-go/datastar"

	"app/internal/tutorial"
)

const stage2Description = `Stage 2 shows that a single depot (<code>activity</code>) can feed many
PState shapes at once: a global scalar counter, a per-user map, a per-user set of tags, an
ordered list of events, and a fixed-key record. ETLs are the only writers to their PStates.`

func (s *Server) addStage2Routes() {
	s.mux.HandleFunc("GET /stage2", func(w http.ResponseWriter, r *http.Request) {
		writePage(w, pageData{
			Title:       "Stage 2 — Depots, ETLs, and PStates",
			Description: stage2Description,
			SignalsJSON: signalsJSON(struct {
				statusSignal
				User          string `json:"user"`
				Tag           string `json:"tag"`
				Score         string `json:"score"`
				InspectUser   string `json:"inspectUser"`
				InspectResult string `json:"inspectResult"`
				SumTag        string `json:"sumTag"`
				SumUsers      string `json:"sumUsers"`
				SumResult     string `json:"sumResult"`
			}{statusSignal: statusSignal{Status: "ready"}}),
			Content: stage2Content,
		})
	})

	s.mux.HandleFunc("POST /stage2/record", func(w http.ResponseWriter, r *http.Request) {
		in, err := readSignals[stage2Input](r)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}

		score, err := strconv.ParseInt(in.Score, 10, 64)
		if err != nil || score <= 0 {
			writeSSEError(w, r, fmt.Errorf("score must be a positive integer"))
			return
		}

		if err := s.stage2.Record(r.Context(), tutorial.Activity{User: in.User, Tag: in.Tag, Score: score}); err != nil {
			writeSSEError(w, r, err)
			return
		}

		out := in
		out.Status = fmt.Sprintf("recorded %s/%s/%d", in.User, in.Tag, score)
		sse := datastar.NewSSE(w, r)
		_ = patchSignals(sse, out)
	})

	s.mux.HandleFunc("POST /stage2/inspect", func(w http.ResponseWriter, r *http.Request) {
		in, err := readSignals[stage2Input](r)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}

		ctx := r.Context()

		total, err := s.stage2.TotalCount(ctx)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}

		score, scoreFound, err := s.stage2.UserScore(ctx, in.InspectUser)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}

		tags, err := s.stage2.UserTags(ctx, in.InspectUser)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}

		events, err := s.stage2.UserEvents(ctx, in.InspectUser)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}

		rec, recFound, err := s.stage2.UserRecord(ctx, in.InspectUser)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}

		resultHTML := stage2InspectHTML(in.InspectUser, total, score, scoreFound, tags, events, rec, recFound)

		out := in
		out.InspectResult = "(see rendered result above)"
		out.Status = fmt.Sprintf("inspected %q", in.InspectUser)
		sse := datastar.NewSSE(w, r)
		_ = sse.PatchElements(resultHTML, datastar.WithSelectorID("stage2-result"), datastar.WithModeInner())
		_ = patchSignals(sse, out)
	})

	s.mux.HandleFunc("POST /stage2/sum", func(w http.ResponseWriter, r *http.Request) {
		in, err := readSignals[stage2Input](r)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}

		users := splitUsers(in.SumUsers)
		sum, err := s.stage2.SumScoresForTag(r.Context(), in.SumTag, users)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}

		out := in
		out.SumResult = fmt.Sprintf("%d", sum)
		out.Status = fmt.Sprintf("summed scores for tag %q", in.SumTag)
		sse := datastar.NewSSE(w, r)
		_ = sse.PatchElements(
			fmt.Sprintf(`<p class="result-card">Sum of scores for users tagged %q: <strong>%d</strong></p>`, htmlEscape(in.SumTag), sum),
			datastar.WithSelectorID("stage2-sum-result"),
			datastar.WithModeInner(),
		)
		_ = patchSignals(sse, out)
	})
}

type stage2Input struct {
	statusSignal
	User          string `json:"user"`
	Tag           string `json:"tag"`
	Score         string `json:"score"`
	InspectUser   string `json:"inspectUser"`
	InspectResult string `json:"inspectResult"`
	SumTag        string `json:"sumTag"`
	SumUsers      string `json:"sumUsers"`
	SumResult     string `json:"sumResult"`
}

func stage2InspectHTML(user string, total uint64, score int64, scoreFound bool, tags, events []string, rec tutorial.UserRecord, recFound bool) string {
	b := &strings.Builder{}
	fmt.Fprintf(b, `<h3>PState view for <code>%s</code></h3>`, htmlEscape(user))
	fmt.Fprintf(b, `<dl class="result-list">`)
	fmt.Fprintf(b, `<dt>Global total count</dt><dd>%d</dd>`, total)
	if scoreFound {
		fmt.Fprintf(b, `<dt>User score</dt><dd>%d</dd>`, score)
	} else {
		fmt.Fprintf(b, `<dt>User score</dt><dd>(none)</dd>`)
	}
	fmt.Fprintf(b, `<dt>Tags</dt><dd>%s</dd>`, renderStringList(tags))
	fmt.Fprintf(b, `<dt>Events</dt><dd>%s</dd>`, renderStringList(events))
	if recFound {
		fmt.Fprintf(b, `<dt>Fixed-key record</dt><dd>events=%d lastTag=%s highScore=%d</dd>`,
			rec.EventCount, htmlEscape(rec.LastTag), rec.HighScore)
	} else {
		fmt.Fprintf(b, `<dt>Fixed-key record</dt><dd>(none)</dd>`)
	}
	fmt.Fprintf(b, `</dl>`)
	return b.String()
}

func renderStringList(items []string) string {
	if len(items) == 0 {
		return `<p class="muted">(none)</p>`
	}
	b := &strings.Builder{}
	b.WriteString(`<ul>`)
	for _, item := range items {
		fmt.Fprintf(b, `<li>%s</li>`, htmlEscape(item))
	}
	b.WriteString(`</ul>`)
	return b.String()
}

func splitUsers(raw string) []string {
	parts := strings.Split(raw, ",")
	users := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			users = append(users, p)
		}
	}
	return users
}

var stage2Content = template.HTML(`
<div class="card">
    <h2>Record an activity</h2>
    <label>User: <input type="text" data-bind:user placeholder="alice" /></label>
    <label>Tag: <input type="text" data-bind:tag placeholder="sports" /></label>
    <label>Score: <input type="number" data-bind:score placeholder="10" /></label>
    <button data-on:click="@post('/stage2/record')">Record</button>
</div>

<div class="card">
    <h2>Inspect PStates for a user</h2>
    <label>User: <input type="text" data-bind:inspectUser placeholder="alice" /></label>
    <button data-on:click="@post('/stage2/inspect')">Inspect</button>
</div>
<div id="stage2-result" class="card">
    <p class="muted">Click Inspect to render the PState view.</p>
</div>

<div class="card">
    <h2>Server-side transform</h2>
    <p>Sum the per-user scores only for users who have a given tag.</p>
    <label>Tag: <input type="text" data-bind:sumTag placeholder="sports" /></label>
    <label>Users (comma-separated): <input type="text" data-bind:sumUsers placeholder="alice, bob, carol" /></label>
    <button data-on:click="@post('/stage2/sum')">Sum</button>
</div>
<div id="stage2-sum-result" class="card result-card">
    <p class="muted">Result will appear here.</p>
</div>
`)
