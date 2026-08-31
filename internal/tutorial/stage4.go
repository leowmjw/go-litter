package tutorial

import "errors"

// Stage 4 - Dataflow Programming.
//
// Rama dataflow topologies are directed graphs of operations where each
// operation receives input and emits zero, one, or many outputs, bindings
// are immutable and scoped, and graphs can branch, join/unify, loop, and
// use reusable custom operations. This project's Go API does not copy
// Rama's Java builder syntax; instead it models the same semantics as a
// small composable Op type so they are directly observable and testable.

// Bindings is an immutable scoped variable frame: each combinator returns a
// *new* Bindings rather than mutating the caller's map, matching Rama's
// immutable-binding semantics.
type Bindings map[string]any

func (b Bindings) with(key string, value any) Bindings {
	next := make(Bindings, len(b)+1)
	for k, v := range b {
		next[k] = v
	}
	next[key] = value
	return next
}

// Op is a dataflow operation: given the current bindings, it emits zero,
// one, or many resulting binding sets. Zero outputs means the input was
// filtered out; more than one means the operation branched.
type Op func(Bindings) ([]Bindings, error)

// Then composes two operations linearly: each output of the first flows
// into the second (a linear pipeline).
func Then(first, second Op) Op {
	return func(in Bindings) ([]Bindings, error) {
		firstOut, err := first(in)
		if err != nil {
			return nil, err
		}
		results := make([]Bindings, 0, len(firstOut))
		for _, mid := range firstOut {
			secondOut, err := second(mid)
			if err != nil {
				return nil, err
			}
			results = append(results, secondOut...)
		}
		return results, nil
	}
}

// Emit binds the result of fn under name, matching a named-output node.
func Emit(name string, fn func(Bindings) (any, error)) Op {
	return func(in Bindings) ([]Bindings, error) {
		value, err := fn(in)
		if err != nil {
			return nil, err
		}
		return []Bindings{in.with(name, value)}, nil
	}
}

// Filter drops the input (emits zero outputs) unless the predicate holds,
// modeling a conditional gate in the graph.
func Filter(predicate func(Bindings) bool) Op {
	return func(in Bindings) ([]Bindings, error) {
		if predicate(in) {
			return []Bindings{in}, nil
		}
		return nil, nil
	}
}

// Branch fans one input out to every branch operation, modeling Rama's
// branch/anchor/hook: all branches observe the same upstream bindings.
func Branch(branches ...Op) Op {
	return func(in Bindings) ([]Bindings, error) {
		results := make([]Bindings, 0, len(branches))
		for _, branch := range branches {
			out, err := branch(in)
			if err != nil {
				return nil, err
			}
			results = append(results, out...)
		}
		return results, nil
	}
}

// ErrUnificationMissingBinding is returned when a binding required after a
// branch point is absent on at least one branch that reaches the
// unification, i.e. the branches disagree on what they produced.
var ErrUnificationMissingBinding = errors.New("binding used after branch is missing on at least one branch")

// Unify merges the outputs of Branch back into single-flow bindings,
// requiring that every named key is present in every branch's output
// (Rama requires a binding used after a branch to be present on every
// branch reaching that point).
func Unify(keys ...string) Op {
	return func(in Bindings) ([]Bindings, error) {
		for _, key := range keys {
			if _, ok := in[key]; !ok {
				return nil, ErrUnificationMissingBinding
			}
		}
		return []Bindings{in}, nil
	}
}

// LoopResult is the outcome of one loop iteration body.
type LoopResult struct {
	Bindings Bindings
	Continue bool // Continue re-enters the loop with these bindings as loop state.
}

// Loop repeatedly applies body to evolving loop state until it reports
// Continue == false or maxIterations is reached, modeling Rama's loop
// state/continue/emit semantics. The final bindings are emitted.
func Loop(body func(Bindings) (LoopResult, error), maxIterations int) Op {
	return func(in Bindings) ([]Bindings, error) {
		state := in
		for i := 0; i < maxIterations; i++ {
			result, err := body(state)
			if err != nil {
				return nil, err
			}
			state = result.Bindings
			if !result.Continue {
				return []Bindings{state}, nil
			}
		}
		return []Bindings{state}, nil
	}
}

// Run executes an Op fragment against fresh bindings; it is the "reusable
// custom operation and graph fragment" entry point used by tests.
func Run(op Op, in Bindings) ([]Bindings, error) {
	if in == nil {
		in = Bindings{}
	}
	return op(in)
}
