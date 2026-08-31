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

const stage4Description = `Stage 4 models Rama dataflow programming as composable operations over
immutable scoped bindings. Operations can emit zero, one, or many outputs; linear pipelines,
branches, unification, conditionals, loops, and reusable custom operations are all first-class.`

func (s *Server) addStage4Routes() {
	s.mux.HandleFunc("GET /stage4", func(w http.ResponseWriter, r *http.Request) {
		writePage(w, pageData{
			Title:       "Stage 4 — Dataflow Programming",
			Description: stage4Description,
			SignalsJSON: signalsJSON(struct {
				statusSignal
				Example string `json:"example"`
				N       string `json:"n"`
				Result  string `json:"result"`
			}{statusSignal: statusSignal{Status: "ready"}, Example: "linear", N: "5"}),
			Content: stage4Content,
		})
	})

	s.mux.HandleFunc("POST /stage4/run", func(w http.ResponseWriter, r *http.Request) {
		in, err := readSignals[stage4Input](r)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}

		n, err := strconv.Atoi(in.N)
		if err != nil {
			writeSSEError(w, r, fmt.Errorf("enter an integer"))
			return
		}

		var op tutorial.Op
		switch in.Example {
		case "linear":
			op = linearPipeline()
		case "branch-unify":
			op = branchUnifyPipeline()
		case "loop":
			op = loopPipeline()
		default:
			writeSSEError(w, r, fmt.Errorf("unknown example %q", in.Example))
			return
		}

		initial := tutorial.Bindings{"n": n}
		if in.Example == "loop" {
			initial["i"] = 1
			initial["sum"] = 0
		}

		outs, err := tutorial.Run(op, initial)
		if err != nil {
			writeSSEError(w, r, err)
			return
		}

		out := in
		out.Result = formatOutputs(outs)
		out.Status = fmt.Sprintf("ran %q with n=%d", in.Example, n)
		sse := datastar.NewSSE(w, r)
		_ = patchSignals(sse, out)
	})
}

func linearPipeline() tutorial.Op {
	return tutorial.Then(
		tutorial.Emit("doubled", func(b tutorial.Bindings) (any, error) {
			return b["n"].(int) * 2, nil
		}),
		tutorial.Emit("plusOne", func(b tutorial.Bindings) (any, error) {
			return b["doubled"].(int) + 1, nil
		}),
	)
}

func branchUnifyPipeline() tutorial.Op {
	return tutorial.Then(
		tutorial.Branch(
			tutorial.Emit("tag", func(tutorial.Bindings) (any, error) { return "left-value", nil }),
			tutorial.Emit("tag", func(tutorial.Bindings) (any, error) { return "right-value", nil }),
		),
		tutorial.Unify("tag"),
	)
}

func loopPipeline() tutorial.Op {
	return tutorial.Loop(func(b tutorial.Bindings) (tutorial.LoopResult, error) {
		i := b["i"].(int)
		sum := b["sum"].(int)
		limit := b["n"].(int)
		if i > limit {
			return tutorial.LoopResult{Bindings: b, Continue: false}, nil
		}
		next := tutorial.Bindings{}
		for k, v := range b {
			next[k] = v
		}
		next["i"] = i + 1
		next["sum"] = sum + i
		return tutorial.LoopResult{Bindings: next, Continue: true}, nil
	}, 1000)
}

func formatOutputs(outs []tutorial.Bindings) string {
	if len(outs) == 0 {
		return "(no output — input was filtered out)"
	}
	b := &strings.Builder{}
	for i, out := range outs {
		fmt.Fprintf(b, "output #%d:\n", i+1)
		for k, v := range out {
			fmt.Fprintf(b, "  %s = %v\n", k, v)
		}
	}
	return b.String()
}

type stage4Input struct {
	statusSignal
	Example string `json:"example"`
	N       string `json:"n"`
	Result  string `json:"result"`
}

var stage4Content = template.HTML(`
<div class="card">
    <h2>Run a dataflow pipeline</h2>
    <label>Example:
        <select data-bind:example>
            <option value="linear">Linear: x → doubled → plusOne</option>
            <option value="branch-unify">Branch + Unify: two branches emit the same key</option>
            <option value="loop">Loop: sum 1..n</option>
        </select>
    </label>
    <label>Input number (n): <input type="number" data-bind:n value="5" /></label>
    <button data-on:click="@post('/stage4/run')">Run</button>
    <pre data-text="$result">...</pre>
</div>
`)
