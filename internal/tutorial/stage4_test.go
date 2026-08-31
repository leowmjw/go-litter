package tutorial

import (
	"errors"
	"testing"
)

func TestStage4LinearPipelineAndNamedOutputs(t *testing.T) {
	pipeline := Then(
		Emit("doubled", func(b Bindings) (any, error) { return b["x"].(int) * 2, nil }),
		Emit("plusOne", func(b Bindings) (any, error) { return b["doubled"].(int) + 1, nil }),
	)
	out, err := Run(pipeline, Bindings{"x": 10})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out) != 1 || out[0]["plusOne"] != 21 || out[0]["doubled"] != 20 {
		t.Fatalf("out = %+v", out)
	}
}

func TestStage4FilterEmitsZeroOrOne(t *testing.T) {
	evenOnly := Filter(func(b Bindings) bool { return b["x"].(int)%2 == 0 })
	out, err := Run(evenOnly, Bindings{"x": 3})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected filtered-out input, got %+v", out)
	}
	out, err = Run(evenOnly, Bindings{"x": 4})
	if err != nil || len(out) != 1 {
		t.Fatalf("out = %+v, %v", out, err)
	}
}

func TestStage4BranchAndUnifyRequireEveryBranchBinding(t *testing.T) {
	// Every branch must set "tag" for a downstream Unify("tag") to succeed.
	branched := Branch(
		Emit("tag", func(Bindings) (any, error) { return "left", nil }),
		Emit("tag", func(Bindings) (any, error) { return "right", nil }),
	)
	pipeline := Then(branched, Unify("tag"))
	out, err := Run(pipeline, Bindings{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 branch outputs, got %d: %+v", len(out), out)
	}
	tags := map[string]bool{}
	for _, b := range out {
		tags[b["tag"].(string)] = true
	}
	if !tags["left"] || !tags["right"] {
		t.Fatalf("tags = %+v", tags)
	}
}

func TestStage4UnifyFailsWhenABranchOmitsTheBinding(t *testing.T) {
	// One branch never sets "tag": unification downstream must fail rather
	// than silently proceed with a missing binding.
	branched := Branch(
		Emit("tag", func(Bindings) (any, error) { return "left", nil }),
		func(in Bindings) ([]Bindings, error) { return []Bindings{in}, nil }, // no "tag"
	)
	pipeline := Then(branched, Unify("tag"))
	_, err := Run(pipeline, Bindings{})
	if !errors.Is(err, ErrUnificationMissingBinding) {
		t.Fatalf("err = %v, want ErrUnificationMissingBinding", err)
	}
}

func TestStage4CustomOperationIsReusable(t *testing.T) {
	// A "custom operation" is just an Op value that can be reused inside
	// multiple graph fragments.
	square := Emit("squared", func(b Bindings) (any, error) { return b["n"].(int) * b["n"].(int), nil })
	fragmentA := Then(square, Emit("plus1", func(b Bindings) (any, error) { return b["squared"].(int) + 1, nil }))
	fragmentB := Then(square, Emit("plus2", func(b Bindings) (any, error) { return b["squared"].(int) + 2, nil }))

	outA, err := Run(fragmentA, Bindings{"n": 3})
	if err != nil || outA[0]["plus1"] != 10 {
		t.Fatalf("fragmentA = %+v, %v", outA, err)
	}
	outB, err := Run(fragmentB, Bindings{"n": 3})
	if err != nil || outB[0]["plus2"] != 11 {
		t.Fatalf("fragmentB = %+v, %v", outB, err)
	}
}

func TestStage4LoopStateContinueAndEmit(t *testing.T) {
	// Sum 1..5 using loop state, continuing until a counter is exhausted,
	// then emitting the final accumulated bindings.
	body := func(b Bindings) (LoopResult, error) {
		i := b["i"].(int)
		sum := b["sum"].(int)
		if i > 5 {
			return LoopResult{Bindings: b, Continue: false}, nil
		}
		next := b.with("sum", sum+i).with("i", i+1)
		return LoopResult{Bindings: next, Continue: true}, nil
	}
	out, err := Run(Loop(body, 100), Bindings{"i": 1, "sum": 0})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(out) != 1 || out[0]["sum"] != 15 {
		t.Fatalf("out = %+v", out)
	}
}

func TestStage4LoopRespectsMaxIterationsGuard(t *testing.T) {
	// An infinite loop body is bounded by maxIterations rather than hanging.
	body := func(b Bindings) (LoopResult, error) {
		return LoopResult{Bindings: b.with("ticks", b["ticks"].(int)+1), Continue: true}, nil
	}
	out, err := Run(Loop(body, 5), Bindings{"ticks": 0})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out[0]["ticks"] != 5 {
		t.Fatalf("ticks = %v, want 5", out[0]["ticks"])
	}
}

func TestStage4BindingsAreImmutableAcrossBranches(t *testing.T) {
	base := Bindings{"shared": 1}
	branched := Branch(
		Emit("a", func(Bindings) (any, error) { return 1, nil }),
		Emit("b", func(Bindings) (any, error) { return 2, nil }),
	)
	out, err := Run(branched, base)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, ok := base["a"]; ok {
		t.Fatal("base bindings were mutated by a branch")
	}
	if len(out) != 2 || out[0]["shared"] != 1 || out[1]["shared"] != 1 {
		t.Fatalf("out = %+v", out)
	}
}
