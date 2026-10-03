package openapi3plan

func buildReport(
	domains []*paramDomain,
	pairs []pairRef,
	offsets pairOffsets,
	feasible []bool,
	valids []validAssignment,
	examined int,
	total int64,
	budgetExhausted bool,
) (*Report, error) {
	report := &Report{
		Examined:        examined,
		Total:           total,
		BudgetExhausted: budgetExhausted,
		Valid:           len(valids),
	}

	// Build every pair row in canonical order and classify it.
	report.Pairs = make([]PairStatus, len(pairs))
	for idx, p := range pairs {
		pair := Pair{
			ParamA:     domains[p.i].param.Name,
			CandidateA: domains[p.i].candidates[p.ai],
			ParamB:     domains[p.j].param.Name,
			CandidateB: domains[p.j].candidates[p.aj],
		}
		status := StatusUndetermined
		switch {
		case feasible[idx]:
			status = StatusFeasible
		case !budgetExhausted:
			// The declared finite product was fully scanned and no valid
			// completion covers this pair: provably infeasible.
			status = StatusInfeasible
		default:
			status = StatusUndetermined
		}
		report.Pairs[idx] = PairStatus{Pair: pair, Status: status}
		switch status {
		case StatusFeasible:
			report.Feasible++
			report.FeasiblePairs = append(report.FeasiblePairs, report.Pairs[idx])
		case StatusInfeasible:
			report.Infeasible++
		case StatusUndetermined:
			report.Undetermined++
		}
	}

	selected := selectCases(domains, offsets, feasible, valids)

	report.Cases = make([]*Case, 0, len(selected))
	covered := make(map[int]bool)
	for _, va := range selected {
		c := &Case{
			Values:  va.values,
			Request: va.request,
		}
		for idx, p := range pairs {
			if feasible[idx] && va.a[p.i] == p.ai && va.a[p.j] == p.aj {
				c.Pairs = append(c.Pairs, report.Pairs[idx].Pair)
				covered[idx] = true
			}
		}
		report.Cases = append(report.Cases, c)
	}
	report.Covered = len(covered)

	return report, nil
}

// selectCases chooses a small deterministic subset of the valid assignments
// covering every feasible pair, then removes redundant cases.
//
// The greedy repeatedly picks the case covering the most currently uncovered
// feasible pairs; ties are broken by canonical assignment order, so equal
// inputs (in any presentation order) always yield the same cases. No claim of
// global minimality is made.
func selectCases(domains []*paramDomain, offsets pairOffsets, feasible []bool, valids []validAssignment) []validAssignment {
	target := make([]int, 0, len(feasible))
	for idx, f := range feasible {
		if f {
			target = append(target, idx)
		}
	}

	// Precompute the feasible-pair set each valid case covers (O(valid*n^2)).
	covers := make([]map[int]bool, len(valids))
	for ci, va := range valids {
		m := make(map[int]bool, len(domains)*len(domains)/2)
		for i := 0; i < len(domains); i++ {
			for j := i + 1; j < len(domains); j++ {
				pi := offsets.index(domains, i, j, va.a[i], va.a[j])
				if feasible[pi] {
					m[pi] = true
				}
			}
		}
		covers[ci] = m
	}

	uncovered := make(map[int]bool, len(target))
	for _, pi := range target {
		uncovered[pi] = true
	}

	chosen := make([]int, 0)
	used := make([]bool, len(valids))

	for len(uncovered) > 0 {
		best := -1
		bestCount := 0
		// valids are already in canonical lexicographic enumeration order; the
		// first maximum seen therefore wins ties deterministically.
		for ci := 0; ci < len(valids); ci++ {
			if used[ci] {
				continue
			}
			count := 0
			for pi := range uncovered {
				if covers[ci][pi] {
					count++
				}
			}
			if count > bestCount {
				bestCount = count
				best = ci
			}
		}
		if best == -1 {
			break
		}
		chosen = append(chosen, best)
		used[best] = true
		for pi := range covers[best] {
			delete(uncovered, pi)
		}
	}

	// With zero or one parameter there are no pairs to cover; keep every valid
	// case so the caller still gets representative requests for each value.
	if len(target) == 0 {
		chosen = make([]int, len(valids))
		for ci := range valids {
			chosen[ci] = ci
		}
	}

	// Redundancy removal in reverse selection order: drop a case when every
	// feasible pair it covers is also covered by another kept case. Iterating
	// the latest selections first preferentially retains the broad, early
	// greedy choices.
	kept := make(map[int]bool, len(chosen))
	for _, ci := range chosen {
		kept[ci] = true
	}
	if len(target) == 0 {
		// Nothing can make a pair-free case redundant.
		out := make([]validAssignment, len(valids))
		copy(out, valids)
		return out
	}
	for k := len(chosen) - 1; k >= 0; k-- {
		ci := chosen[k]
		redundant := true
		for pi := range covers[ci] {
			if !coveredByAnother(pi, ci, kept, covers) {
				redundant = false
				break
			}
		}
		if redundant {
			delete(kept, ci)
		}
	}

	out := make([]validAssignment, 0, len(kept))
	for _, ci := range chosen {
		if kept[ci] {
			out = append(out, valids[ci])
		}
	}
	return out
}

func coveredByAnother(pair, self int, kept map[int]bool, covers []map[int]bool) bool {
	for ci := range kept {
		if ci == self {
			continue
		}
		if covers[ci][pair] {
			return true
		}
	}
	return false
}
