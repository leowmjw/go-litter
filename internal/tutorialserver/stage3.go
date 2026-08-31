package tutorialserver

import (
	"fmt"
	"html/template"
	"net/http"

	"github.com/starfederation/datastar-go/datastar"

	"app/internal/tutorial"
)

const stage3Description = `Stage 3 shows distributed programming: a module is split into a fixed
number of logical tasks. Records on one depot partition keep append order, but unrelated
partitions can process in parallel. When an effect belongs to a different key, the ETL can
write directly to that task's PState partition (repartitioning), keeping related state co-located.`

func (s *Server) addStage3Routes() {
	s.mux.HandleFunc("GET /stage3", func(w http.ResponseWriter, r *http.Request) {
		writePage(w, pageData{
			Title:       "Stage 3 — Distributed Programming",
			Description: stage3Description,
			SignalsJSON: signalsJSON(struct {
				statusSignal
				Sender      string `json:"sender"`
				Recipient   string `json:"recipient"`
				Message     string `json:"message"`
				InspectUser string `json:"inspectUser"`
			}{statusSignal: statusSignal{Status: "ready"}}),
			Content: stage3Content,
		})
	})

	s.mux.HandleFunc("POST /stage3/send", func(w http.ResponseWriter, r *http.Request) {
		in, err := readSignals[stage3Input](r)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}

		if err := s.stage3.Send(r.Context(), tutorial.Mail{Sender: in.Sender, Recipient: in.Recipient, Message: in.Message}); err != nil {
			writeSSEErrorWithResult(w, r, "stage3-result", err)
			return
		}

		out := in
		out.Status = fmt.Sprintf("sent from %q to %q (task %d → task %d)",
			in.Sender, in.Recipient, s.stage3.TaskOf(in.Sender), s.stage3.TaskOf(in.Recipient))
		sse := datastar.NewSSE(w, r)
		_ = patchSignals(sse, out)
	})

	s.mux.HandleFunc("POST /stage3/inspect", func(w http.ResponseWriter, r *http.Request) {
		in, err := readSignals[stage3Input](r)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}

		ctx := r.Context()
		count, err := s.stage3.SentCount(ctx, in.InspectUser)
		if err != nil {
			writeSSEErrorWithResult(w, r, "stage3-result", err)
			return
		}
		inbox, err := s.stage3.Inbox(ctx, in.InspectUser)
		if err != nil {
			writeSSEErrorWithResult(w, r, "stage3-result", err)
			return
		}

		out := in
		out.Status = fmt.Sprintf("inspected %q (task %d)", in.InspectUser, s.stage3.TaskOf(in.InspectUser))
		sse := datastar.NewSSE(w, r)
		_ = sse.PatchElements(
			stage3InspectHTML(in.InspectUser, s.stage3.TaskOf(in.InspectUser), count, inbox),
			datastar.WithSelectorID("stage3-result"),
			datastar.WithModeInner(),
		)
		_ = patchSignals(sse, out)
	})
}

func stage3InspectHTML(user string, task uint32, count uint64, inbox []string) string {
	return fmt.Sprintf(
		`<h3>%s lives on task %d</h3><dl class="result-list"><dt>Sent count</dt><dd>%d</dd><dt>Inbox</dt><dd>%s</dd></dl>`,
		htmlEscape(user), task, count, renderStringList(inbox),
	)
}

type stage3Input struct {
	statusSignal
	Sender      string `json:"sender"`
	Recipient   string `json:"recipient"`
	Message     string `json:"message"`
	InspectUser string `json:"inspectUser"`
}

var stage3Content = template.HTML(`
<div class="card">
    <h2>Send mail</h2>
    <p>The depot is partitioned by <strong>sender</strong>; the mailbox PState is on the recipient's task.</p>
    <label>Sender: <input type="text" data-bind:sender placeholder="alice" /></label>
    <label>Recipient: <input type="text" data-bind:recipient placeholder="bob" /></label>
    <label>Message: <input type="text" data-bind:message placeholder="hello" /></label>
    <button data-on:click="@post('/stage3/send')">Send</button>
</div>

<div class="card">
    <h2>Inspect a user</h2>
    <label>User: <input type="text" data-bind:inspectUser placeholder="bob" /></label>
    <button data-on:click="@post('/stage3/inspect')">Inspect</button>
</div>
<div id="stage3-result" class="card">
    <p class="muted">Inspect a user to see their partitioned mailbox and task assignment.</p>
</div>
`)
