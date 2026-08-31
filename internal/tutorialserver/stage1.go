package tutorialserver

import (
	"fmt"
	"html/template"
	"net/http"

	"github.com/starfederation/datastar-go/datastar"

	"app/internal/tutorial"
)

const stage1Description = `Stage 1 introduces the smallest possible Rama module: one depot
(<code>greetings</code>), one stream ETL, one PState, and a point query. Every append to the
depot is durably logged, then the ETL writes the latest message into the PState keyed by name.`

func (s *Server) addStage1Routes() {
	s.mux.HandleFunc("GET /stage1", func(w http.ResponseWriter, r *http.Request) {
		writePage(w, pageData{
			Title:       "Stage 1 — First Module",
			Description: stage1Description,
			SignalsJSON: signalsJSON(struct {
				statusSignal
				Name    string `json:"name"`
				Message string `json:"message"`
				Last    string `json:"last"`
			}{statusSignal: statusSignal{Status: "ready"}}),
			Content: stage1Content,
		})
	})

	s.mux.HandleFunc("POST /stage1/say", func(w http.ResponseWriter, r *http.Request) {
		in, err := readSignals[stage1Input](r)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}

		ctx := r.Context()
		g := tutorial.Greeting{Name: in.Name, Message: in.Message}
		if err := s.stage1.Say(ctx, g); err != nil {
			writeSSEError(w, r, err)
			return
		}

		last, _, err := s.stage1.LastMessage(ctx, in.Name)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}

		out := stage1Input{
			statusSignal: statusSignal{Status: fmt.Sprintf("stored greeting for %q", in.Name)},
			Name:         in.Name,
			Message:      in.Message,
			Last:         last,
		}
		sse := datastar.NewSSE(w, r)
		_ = sse.PatchElements(
			fmt.Sprintf(`<div class="result-card"><strong>%s</strong> said: %s</div>`, htmlEscape(in.Name), htmlEscape(in.Message)),
			datastar.WithSelectorID("stage1-result"),
			datastar.WithModeInner(),
		)
		_ = patchSignals(sse, out)
	})
}

type stage1Input struct {
	statusSignal
	Name    string `json:"name"`
	Message string `json:"message"`
	Last    string `json:"last"`
}

var stage1Content = template.HTML(`
<div class="card">
    <h2>Say something</h2>
    <label>Name: <input type="text" data-bind:name placeholder="alice" /></label>
    <label>Message: <input type="text" data-bind:message placeholder="hello world" /></label>
    <button data-on:click="@post('/stage1/say')">Say</button>
</div>
<div id="stage1-result" class="card result-card">
    <p>No greeting recorded yet.</p>
</div>
`)

func writeSSEError(w http.ResponseWriter, r *http.Request, err error) {
	sse := datastar.NewSSE(w, r)
	_ = sse.MarshalAndPatchSignals(statusSignal{Status: errorStatus(err)})
}
