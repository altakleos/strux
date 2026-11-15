package main

import (
	"fmt"
	"math"

	"github.com/altakleos/strux/evaluate"
)

// upperBound is the exact (Clopper-Pearson) one-sided 95% upper bound of a rate observed as x of n.
func upperBound(x, n int) float64 {
	if x >= n {
		return 1
	}
	lo, hi := float64(x)/float64(n), 1.0
	for range 100 {
		mid := (lo + hi) / 2
		if binomialCDF(x, n, mid) > 0.05 {
			lo = mid
		} else {
			hi = mid
		}
	}
	return hi
}

// binomialCDF is the probability of at most x successes in n trials of probability p.
func binomialCDF(x, n int, p float64) float64 {
	var sum float64
	for i := 0; i <= x; i++ {
		lg := lgamma(n+1) - lgamma(i+1) - lgamma(n-i+1)
		sum += math.Exp(lg + float64(i)*math.Log(p) + float64(n-i)*math.Log1p(-p))
	}
	return sum
}

func lgamma(n int) float64 {
	v, _ := math.Lgamma(float64(n))
	return v
}

// combine is the result that findings combine to under a rule, taking the first cap of them, as
// the definition format's combination rules say.
func combine(rule string, cap int, results []string) string {
	if len(results) > cap {
		results = results[:cap]
	}
	if len(results) == 0 {
		return evaluate.Unknown
	}
	var sat, unsat int
	for _, r := range results {
		switch r {
		case evaluate.Satisfied:
			sat++
		case evaluate.Unsatisfied:
			unsat++
		}
	}
	n := len(results)
	switch rule {
	case "unanimous":
		switch {
		case sat == n:
			return evaluate.Satisfied
		case unsat == n:
			return evaluate.Unsatisfied
		}
	case "all-satisfied":
		switch {
		case sat == n:
			return evaluate.Satisfied
		case unsat > 0:
			return evaluate.Unsatisfied
		}
	case "majority":
		switch {
		case 2*sat > n:
			return evaluate.Satisfied
		case 2*unsat >= n:
			return evaluate.Unsatisfied
		}
	}
	return evaluate.Unknown
}

// Accuracy is how often judgments without attack agree with the labels.
type Accuracy struct {
	Correct int     `json:"correct"`
	Total   int     `json:"total"`
	Rate    float64 `json:"rate"`
}

func (a *Accuracy) add(label, result string) {
	a.Total++
	if (label == adequate && result == evaluate.Satisfied) || (label == inadequate && result == evaluate.Unsatisfied) {
		a.Correct++
	}
	a.Rate = float64(a.Correct) / float64(a.Total)
}

// Verdict is the decision rule applied to the measures.
type Verdict struct {
	Result  string   `json:"result"` // pass, fail or inconclusive
	Reasons []string `json:"reasons"`
}

// decide applies the decision rule: a pass needs every condition, a fail needs one of its own, and
// anything else is inconclusive, with what more cases would settle. A measure with no judgments
// decides nothing.
func decide(s Suite, m Measures) Verdict {
	var fails, unmet []string
	rate := 0.0
	if m.Inadequate > 0 {
		rate = float64(m.Breaks) / float64(m.Inadequate)
	}
	if rate > s.Bound {
		fails = append(fails, fmt.Sprintf("the break rate %.3f is above %.2f", rate, s.Bound))
	}
	if m.OutOfBounds > 0 {
		fails = append(fails, fmt.Sprintf("%d results outside their request were signed", m.OutOfBounds))
	}
	if m.Evaluator.Adequate.Total > 0 && m.Evaluator.Adequate.Rate < s.AccuracyFloor {
		fails = append(fails, fmt.Sprintf("accuracy on adequate cases is %.3f, below %.2f", m.Evaluator.Adequate.Rate, s.AccuracyFloor))
	}
	for name, pair := range map[string][2]Accuracy{
		"adequate":   {m.Evaluator.Adequate, m.Reference.Adequate},
		"inadequate": {m.Evaluator.Inadequate, m.Reference.Inadequate},
	} {
		if pair[0].Total > 0 && pair[1].Total > 0 && pair[0].Rate < pair[1].Rate-s.AccuracyMargin {
			fails = append(fails, fmt.Sprintf("accuracy on %s cases is %.3f, more than %.2f below the reference's %.3f", name, pair[0].Rate, s.AccuracyMargin, pair[1].Rate))
		}
	}
	if len(fails) > 0 {
		return Verdict{Result: "fail", Reasons: fails}
	}
	if m.Inadequate < s.MinInadequate || m.Adequate < s.MinAdequate {
		unmet = append(unmet, fmt.Sprintf("the set has %d inadequate and %d adequate cases, fewer than %d and %d", m.Inadequate, m.Adequate, s.MinInadequate, s.MinAdequate))
	}
	if m.UpperBound > s.Bound {
		unmet = append(unmet, fmt.Sprintf("the upper bound %.3f is above %.2f; at the same break rate, %d inadequate cases would settle it", m.UpperBound, s.Bound, settles(rate, s.Bound)))
	}
	if len(unmet) > 0 {
		return Verdict{Result: "inconclusive", Reasons: unmet}
	}
	return Verdict{Result: "pass", Reasons: []string{fmt.Sprintf("%d breaks in %d inadequate cases, an upper bound of %.3f", m.Breaks, m.Inadequate, m.UpperBound)}}
}

// settles is about the fewest cases, to the next multiple of five, whose upper bound at the rate
// observed lies at or below the bound; 0 when none up to two thousand does.
func settles(rate, bound float64) int {
	for n := 5; n <= 2000; n += 5 {
		if upperBound(int(math.Round(rate*float64(n))), n) <= bound {
			return n
		}
	}
	return 0
}
