package tutorialserver

import (
	"fmt"
	"html/template"
	"net/http"

	"github.com/starfederation/datastar-go/datastar"
)

const stage5Description = `Stage 5 contrasts stream and microbatch ETLs using the same event. A
stream append is acknowledged only after its PState effect is committed. A microbatch append
only durably logs the record; you must explicitly advance the batch before the effect becomes
visible. Microbatches provide exactly-once committed effects.`

func (s *Server) addStage5Routes() {
	s.mux.HandleFunc("GET /stage5", func(w http.ResponseWriter, r *http.Request) {
		writePage(w, pageData{
			Title:       "Stage 5 — Stream vs Microbatch ETLs",
			Description: stage5Description,
			SignalsJSON: signalsJSON(struct {
				statusSignal
				Key             string `json:"key"`
				StreamCount     string `json:"streamCount"`
				MicrobatchCount string `json:"microbatchCount"`
			}{statusSignal: statusSignal{Status: "ready"}, Key: "a", StreamCount: "0", MicrobatchCount: "0"}),
			Content: stage5Content,
		})
	})

	s.mux.HandleFunc("POST /stage5/stream-ping", func(w http.ResponseWriter, r *http.Request) {
		in, err := readSignals[stage5Input](r)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}
		ctx := r.Context()
		if err := s.stage5Stream.Append(ctx, in.Key); err != nil {
			writeSSEError(w, r, err)
			return
		}
		count, err := s.stage5Stream.Count(ctx, in.Key)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}
		out := in
		out.StreamCount = fmt.Sprintf("%d", count)
		out.Status = fmt.Sprintf("stream append for %q completed and is visible", in.Key)
		sse := datastar.NewSSE(w, r)
		_ = patchSignals(sse, out)
	})

	s.mux.HandleFunc("POST /stage5/microbatch-ping", func(w http.ResponseWriter, r *http.Request) {
		in, err := readSignals[stage5Input](r)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}
		if err := s.stage5Micro.Append(r.Context(), in.Key); err != nil {
			writeSSEError(w, r, err)
			return
		}
		out := in
		out.Status = fmt.Sprintf("microbatch append for %q logged; click Advance to process", in.Key)
		sse := datastar.NewSSE(w, r)
		_ = patchSignals(sse, out)
	})

	s.mux.HandleFunc("POST /stage5/advance", func(w http.ResponseWriter, r *http.Request) {
		in, err := readSignals[stage5Input](r)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}
		ctx := r.Context()
		n, err := s.stage5Micro.Advance(ctx)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}
		count, err := s.stage5Micro.Count(ctx, in.Key)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}
		out := in
		out.MicrobatchCount = fmt.Sprintf("%d", count)
		out.Status = fmt.Sprintf("advanced microbatch; processed %d partition batch(es)", n)
		sse := datastar.NewSSE(w, r)
		_ = patchSignals(sse, out)
	})
}

type stage5Input struct {
	statusSignal
	Key             string `json:"key"`
	StreamCount     string `json:"streamCount"`
	MicrobatchCount string `json:"microbatchCount"`
}

var stage5Content = template.HTML(`
<div class="card">
    <h2>Compare append semantics</h2>
    <label>Key: <input type="text" data-bind:key value="a" /></label>
    <button data-on:click="@post('/stage5/stream-ping')">Stream Ping</button>
    <button data-on:click="@post('/stage5/microbatch-ping')">Microbatch Ping</button>
    <button data-on:click="@post('/stage5/advance')">Advance Microbatch</button>
    <p>Stream count: <code data-text="$streamCount">0</code> — visible immediately after acknowledge.</p>
    <p>Microbatch count: <code data-text="$microbatchCount">0</code> — visible only after Advance.</p>
</div>
`)
