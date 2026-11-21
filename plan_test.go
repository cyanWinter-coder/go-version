// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: MPL-2.0

package version

import (
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
)

func mustVersion(t *testing.T, raw string) *Version {
	t.Helper()

	v, err := NewVersion(raw)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	return v
}

func mustVersions(t *testing.T, raws ...string) []*Version {
	t.Helper()

	versions := make([]*Version, len(raws))
	for i, raw := range raws {
		versions[i] = mustVersion(t, raw)
	}

	return versions
}

func mustGroup(t *testing.T, name, expr string) ConstraintGroup {
	t.Helper()

	g, err := NewConstraintGroup(name, expr)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	return g
}

func stringsOf(versions []*Version) []string {
	result := make([]string, len(versions))
	for i, v := range versions {
		result[i] = v.String()
	}

	return result
}

func TestConstraintGroup(t *testing.T) {
	g := mustGroup(t, "runtime", ">= 1.0, < 2.0")

	if !g.Check(mustVersion(t, "1.5.0")) {
		t.Fatal("expected 1.5.0 to satisfy the group")
	}
	if g.Check(mustVersion(t, "2.0.0")) {
		t.Fatal("expected 2.0.0 to fail the group")
	}
	if got, want := g.String(), "runtime: >= 1.0, < 2.0"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}

	if _, err := NewConstraintGroup("bad", ">= 1.x"); err == nil {
		t.Fatal("expected a parse error")
	}

	groups, err := NewConstraintGroups(
		ConstraintGroupExpr{Name: "a", Expr: ">= 1.0"},
		ConstraintGroupExpr{Name: "b", Expr: "< 2.0"},
	)
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if len(groups) != 2 || groups[1].Name != "b" {
		t.Fatalf("unexpected groups: %#v", groups)
	}

	if _, err := NewConstraintGroups(
		ConstraintGroupExpr{Name: "a", Expr: ">= 1.0"},
		ConstraintGroupExpr{Name: "b", Expr: ">= 1.x"},
	); err == nil {
		t.Fatal("expected NewConstraintGroups to reject the whole input")
	}
}

func TestPlanFeasible(t *testing.T) {
	candidates := mustVersions(t,
		"1.4.2", "1.0.0", "1.2.0", "2.0.0", "1.2.5", "1.3.0-beta.1", "v1.1.0",
	)
	groups := []ConstraintGroup{
		mustGroup(t, "runtime", ">= 1.1, < 1.4"),
		mustGroup(t, "api", ">= 1.2.0"),
	}

	result := Plan(candidates, groups)

	if got, want := stringsOf(result.Feasible), []string{"1.2.0", "1.2.5"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("feasible: got %v want %v", got, want)
	}
	if result.Lowest == nil || result.Lowest.String() != "1.2.0" {
		t.Fatalf("lowest: %#v", result.Lowest)
	}
	if result.Highest == nil || result.Highest.String() != "1.2.5" {
		t.Fatalf("highest: %#v", result.Highest)
	}
	if !result.HasFeasible() {
		t.Fatal("expected HasFeasible to be true")
	}
	if result.Reason != ReasonFeasible {
		t.Fatalf("unexpected reason: %s (%s)", result.Reason, result.Detail)
	}
	if len(result.Conflicts) != 0 {
		t.Fatalf("unexpected conflicts: %#v", result.Conflicts)
	}
	if result.CandidateLow.String() != "1.0.0" || result.CandidateHigh.String() != "2.0.0" {
		t.Fatalf("candidate bounds: %s .. %s", result.CandidateLow, result.CandidateHigh)
	}
}

// TestPlanMatchesConstraintCheck pins planning to the existing per-version
// constraint semantics for prereleases, prefixes and metadata.
func TestPlanMatchesConstraintCheck(t *testing.T) {
	type candidate struct {
		raw  string
		opts []Option
	}

	cases := []struct {
		name       string
		expr       string
		candidates []candidate
		want       []string
	}{
		{
			name: "prerelease excluded by release bound",
			expr: ">= 1.0.0, < 2.0.0",
			candidates: []candidate{
				{raw: "1.0.0"}, {raw: "1.5.0-beta.1"}, {raw: "1.9.9"}, {raw: "2.0.0"},
			},
			want: []string{"1.0.0", "1.9.9"},
		},
		{
			name: "prerelease bound only matches same base segments",
			expr: ">= 1.5.0-beta.2",
			candidates: []candidate{
				{raw: "1.5.0-beta.1"}, {raw: "1.5.0-beta.3"}, {raw: "1.5.0"}, {raw: "1.6.0-beta.1"},
			},
			want: []string{"1.5.0-beta.3", "1.5.0"},
		},
		{
			name: "pessimistic prerelease",
			expr: "~> 2.1.0-a",
			candidates: []candidate{
				{raw: "2.1.0-a.1"}, {raw: "2.1.0-beta"}, {raw: "2.1.0"}, {raw: "2.2.0-alpha"},
			},
			want: []string{"2.1.0-a.1", "2.1.0-beta"},
		},
		{
			name: "prefix is stripped before comparison",
			expr: ">= 1.2.0, < 1.3.0",
			candidates: []candidate{
				{raw: "deployment-v1.1.9", opts: []Option{WithPrefix("deployment-")}},
				{raw: "deployment-v1.2.3", opts: []Option{WithPrefix("deployment-")}},
			},
			want: []string{"1.2.3"},
		},
		{
			name: "metadata does not affect ordering",
			expr: ">= 1.0.0, <= 1.0.0+zzz",
			candidates: []candidate{
				{raw: "1.0.0+aaa"}, {raw: "1.0.0+zzz"}, {raw: "1.0.1"},
			},
			want: []string{"1.0.0+aaa", "1.0.0+zzz"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			constraints, err := NewConstraint(tc.expr)
			if err != nil {
				t.Fatalf("err: %s", err)
			}

			var candidates []*Version
			for _, c := range tc.candidates {
				v, err := NewVersion(c.raw, c.opts...)
				if err != nil {
					t.Fatalf("err: %s", err)
				}
				candidates = append(candidates, v)
			}

			result := Plan(candidates, []ConstraintGroup{{Name: "c", Constraints: constraints}})
			if got := stringsOf(result.Feasible); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}

			planned := make(map[*Version]bool)
			for _, f := range result.Feasible {
				planned[f] = true
			}
			for _, v := range candidates {
				if planned[v] != constraints.Check(v) {
					t.Fatalf("%s: plan=%v Check=%v", v, planned[v], constraints.Check(v))
				}
			}
		})
	}
}

func TestPlanNoFeasibleReportsConflict(t *testing.T) {
	candidates := mustVersions(t, "1.0.0", "1.4.0", "1.5.0", "2.0.0")
	groups := []ConstraintGroup{
		mustGroup(t, "runtime", ">= 1.5"),
		mustGroup(t, "api", "< 1.2"),
	}

	result := Plan(candidates, groups)

	if result.HasFeasible() {
		t.Fatalf("unexpected feasible versions: %v", stringsOf(result.Feasible))
	}
	if result.Feasible == nil {
		t.Fatal("Feasible must be non-nil")
	}
	if result.Lowest != nil || result.Highest != nil {
		t.Fatalf("expected nil bounds, got %v .. %v", result.Lowest, result.Highest)
	}
	if result.Reason != ReasonConflictingConstraints {
		t.Fatalf("unexpected reason: %s (%s)", result.Reason, result.Detail)
	}
	if len(result.Conflicts) == 0 {
		t.Fatalf("expected conflicts, reason=%q", result.Reason)
	}

	c := result.Conflicts[0]
	if !reflect.DeepEqual(c.Groups, []string{"runtime", "api"}) {
		t.Fatalf("unexpected conflict groups: %v", c.Groups)
	}
	if c.Lower == nil || c.Lower.String() != "1.5.0" {
		t.Fatalf("lower bound: %v", c.Lower)
	}
	if c.Upper == nil || c.Upper.String() != "1.2.0" {
		t.Fatalf("upper bound: %v", c.Upper)
	}
	if len(c.Constraints) != 2 {
		t.Fatalf("unexpected constraints: %v", c.Constraints)
	}
	if !strings.Contains(c.String(), "runtime") || !strings.Contains(c.String(), "api") {
		t.Fatalf("conflict string lacks group names: %s", c.String())
	}
	if !strings.Contains(result.Detail, "greater than") {
		t.Fatalf("detail lacks the boundary explanation: %s", result.Detail)
	}
}

func TestPlanConflictDetection(t *testing.T) {
	cases := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{"disjoint", "< 1.0", "> 2.0", true},
		{"exclusive equal", "> 1.0", "< 1.0", true},
		{"exclusive lower at same value", "> 1.0", "<= 1.0", true},
		{"inclusive equal", ">= 1.0", "<= 1.0", false},
		{"point plus exclusion", "= 1.0", "!= 1.0", true},
		{"not equals alone", "!= 1.0", ">= 1.0", false},
		{"two different exclusions", "!= 1.0", "!= 2.0", false},
		{"prerelease pessimistic vs release point", "~> 1.2.0-a", "= 1.2.0", true},
		{"pessimistic disjoint", "~> 1.2", "~> 2.0", true},
		{"pessimistic overlapping", "~> 1.2", "~> 1.2.5", false},
		{"prerelease and release bound", ">= 1.0.0-a", "= 1.0.0", false},
		{"two disjoint points", "= 1.0", "= 2.0", true},
		{"equal points", "= 1.0", "= 1.0.0", false},
		{"exclusion outside range", ">= 1.0, <= 1.0", "!= 2.0", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			groups := []ConstraintGroup{
				mustGroup(t, "a", tc.a),
				mustGroup(t, "b", tc.b),
			}

			result := Plan(mustVersions(t, "1.0.0"), groups)
			got := result.Reason == ReasonConflictingConstraints
			if got != tc.want {
				if got {
					t.Fatalf("%q vs %q: unexpected conflict %v", tc.a, tc.b, result.Conflicts)
				}
				t.Fatalf("%q vs %q: expected a conflict, got %s", tc.a, tc.b, result.Detail)
			}

			if got {
				// The conflict must name both groups and carry bounds that are
				// actually contradictory.
				conflict := result.Conflicts[0]
				seen := map[string]bool{}
				for _, g := range conflict.Groups {
					seen[g] = true
				}
				if len(conflict.Groups) != 2 || !seen["a"] || !seen["b"] {
					t.Fatalf("%q vs %q: unexpected groups %v", tc.a, tc.b, conflict.Groups)
				}
				if conflict.Lower == nil && conflict.Upper == nil {
					t.Fatalf("%q vs %q: conflict has no bounds: %s", tc.a, tc.b, conflict)
				}
				if conflict.Detail == "" {
					t.Fatalf("%q vs %q: conflict has no detail", tc.a, tc.b)
				}
			}
		})
	}
}

// TestPlanNoFalseConflicts guards against reporting a conflict when Compare's
// equality quirks (extra zero segments, prerelease specificity) leave a
// satisfying version that a naive bound comparison would miss.
func TestPlanNoFalseConflicts(t *testing.T) {
	cases := []struct {
		name    string
		group   string
		witness string
	}{
		{"equal prefix extra segment", "= 1.0.0.0, != 1.0.0", "1.0.0-alpha"},
		{"equal prerelease vs release range", "= 1.0.0.0-alpha, >= 1.0.0", "1.0.0"},
		{"equal release vs pessimistic prerelease", "= 1.0.0, ~> 1.0.0.0-alpha", "1.0.0.0-alpha"},
		{"two equals different specificity", "= 1.0.0, = 1.0.0.0-alpha", "1.0.0.0-alpha"},
		{"prerelease eq vs release gte long", "= 1.0.0-alpha, >= 1.0.0.0", "1.0.0.0"},
		{"prerelease eq vs release gte", "= 1.0.0-alpha, >= 1.0.0", "1.0.0.0"},
		{"release upper below prerelease", ">= 1.0.0, <= 1.0.0-alpha", "1.0.0.0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := mustGroup(t, "runtime", tc.group)
			witness := mustVersion(t, tc.witness)

			if !g.Check(witness) {
				t.Fatalf("test premise broken: %s does not satisfy %s", witness, tc.group)
			}

			result := Plan([]*Version{witness}, []ConstraintGroup{g})
			if !result.HasFeasible() {
				t.Fatalf("%s should be feasible for %s: %s", witness, tc.group, result.Detail)
			}
			if result.Reason != ReasonFeasible {
				t.Fatalf("unexpected reason for satisfiable %s: %v", tc.group, result.Conflicts)
			}

			// The same constraints, without the witness, must not claim a
			// contradiction either.
			empty := Plan(nil, []ConstraintGroup{g})
			if empty.Reason == ReasonConflictingConstraints {
				t.Fatalf("unexpected conflict with no candidates for %s: %v", tc.group, empty.Conflicts)
			}
		})
	}
}

func TestPlanNoFeasibleThreeWayExclusion(t *testing.T) {
	candidates := mustVersions(t, "1.0.0", "1.5.0", "2.0.0")
	groups := []ConstraintGroup{
		mustGroup(t, "runtime", ">= 2.0"),
		mustGroup(t, "api", "<= 2.0"),
		mustGroup(t, "blocklist", "!= 2.0.0"),
	}

	result := Plan(candidates, groups)
	if result.HasFeasible() {
		t.Fatalf("unexpected feasible versions: %v", stringsOf(result.Feasible))
	}
	if result.Reason != ReasonConflictingConstraints || len(result.Conflicts) == 0 {
		t.Fatalf("expected conflicts, reason=%s detail=%q", result.Reason, result.Detail)
	}

	conflict := result.Conflicts[0]
	if conflict.Lower == nil || conflict.Upper == nil {
		t.Fatalf("expected both bounds, got %#v", conflict)
	}
	if !conflict.Lower.Equal(conflict.Upper) {
		t.Fatalf("expected a point range, got %s .. %s", conflict.Lower, conflict.Upper)
	}
	if len(conflict.Constraints) == 0 || len(conflict.Groups) < 2 {
		t.Fatalf("expected conflict members, got %#v", conflict)
	}
	if len(conflict.Excluded) == 0 {
		t.Fatalf("expected the exclusion to be reported, got %#v", conflict)
	}
}

func TestPlanNoFeasibleCandidatesOutOfRange(t *testing.T) {
	candidates := mustVersions(t, "0.1.0", "0.2.0")
	groups := []ConstraintGroup{mustGroup(t, "runtime", ">= 1.0")}

	result := Plan(candidates, groups)
	if result.HasFeasible() {
		t.Fatalf("unexpected feasible versions: %v", stringsOf(result.Feasible))
	}
	if result.Reason != ReasonNoMatchingCandidate {
		t.Fatalf("unexpected reason: %s (%s)", result.Reason, result.Detail)
	}
	if len(result.Conflicts) != 1 {
		t.Fatalf("expected one diagnostic conflict, got %#v", result.Conflicts)
	}
	if !strings.Contains(result.Detail, "1.0.0") || !strings.Contains(result.Detail, "0.1.0") {
		t.Fatalf("detail should mention bounds and candidates: %s", result.Detail)
	}
	if result.LowerBound == nil || result.LowerBound.String() != "1.0.0" || !result.LowerBoundInclusive {
		t.Fatalf("lower bound: %v inclusive=%v", result.LowerBound, result.LowerBoundInclusive)
	}
	if result.UpperBound != nil {
		t.Fatalf("upper bound: %v", result.UpperBound)
	}
}

func TestPlanIdempotentAndStable(t *testing.T) {
	candidates := mustVersions(t,
		"1.2.0", "1.0.0", "1.2.0", "v1.2.0", "1.2.0+build.2", "1.0.0", "1.1.0+meta",
	)
	group := mustGroup(t, "runtime", ">= 1.0.0, < 2.0.0")

	first := Plan(candidates, []ConstraintGroup{group})
	second := Plan(candidates, []ConstraintGroup{group})

	if !reflect.DeepEqual(stringsOf(first.Feasible), stringsOf(second.Feasible)) {
		t.Fatalf("results differ: %v vs %v", stringsOf(first.Feasible), stringsOf(second.Feasible))
	}
	// 1.2.0 appears three times canonically and is collapsed to one entry.
	want := []string{"1.0.0", "1.1.0+meta", "1.2.0", "1.2.0+build.2"}
	if got := stringsOf(first.Feasible); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}

	// The caller's slice must not be reordered or mutated.
	if got := candidates[0].String(); got != "1.2.0" {
		t.Fatalf("input was mutated: first is %s", got)
	}

	// Repeating an equivalent constraint in another group is a no-op.
	repeated := Plan(candidates, []ConstraintGroup{
		group,
		mustGroup(t, "runtime-copy", ">= 1.0.0, < 2.0.0"),
	})
	if !reflect.DeepEqual(stringsOf(repeated.Feasible), want) {
		t.Fatalf("got %v want %v", stringsOf(repeated.Feasible), want)
	}

	// Reversing the input produces the same order, including the tie between
	// versions that compare equal (metadata is ignored), because the sort
	// breaks ties canonically.
	reversed := Plan([]*Version{
		candidates[6], candidates[5], candidates[4], candidates[3],
		candidates[2], candidates[1], candidates[0],
	}, []ConstraintGroup{group})
	if got := stringsOf(reversed.Feasible); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPlanEmptyCandidates(t *testing.T) {
	group := mustGroup(t, "runtime", ">= 1.0")

	for _, name := range []string{"nil candidates", "empty candidates"} {
		t.Run(name, func(t *testing.T) {
			var candidates []*Version
			if name == "empty candidates" {
				candidates = []*Version{}
			}

			result := Plan(candidates, []ConstraintGroup{group})
			if result == nil {
				t.Fatal("result is nil")
			}
			if result.Feasible == nil {
				t.Fatal("Feasible must be non-nil")
			}
			if result.HasFeasible() {
				t.Fatalf("unexpected feasible versions: %v", stringsOf(result.Feasible))
			}
			if result.Lowest != nil || result.Highest != nil {
				t.Fatal("expected nil bounds")
			}
			if result.CandidateLow != nil || result.CandidateHigh != nil {
				t.Fatal("expected nil candidate bounds")
			}
			if result.Reason != ReasonNoCandidates {
				t.Fatalf("unexpected reason: %s", result.Reason)
			}
			if !strings.Contains(result.Detail, "no candidate versions") {
				t.Fatalf("unexpected detail: %s", result.Detail)
			}
		})
	}
}

func TestPlanEmptyConstraints(t *testing.T) {
	groups := []ConstraintGroup{
		{Name: "noop"},
		{Name: "also-noop", Constraints: Constraints{}},
	}

	for _, tc := range []struct {
		name   string
		groups []ConstraintGroup
	}{
		{"nil groups", nil},
		{"empty groups", []ConstraintGroup{}},
		{"empty constraints", groups},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := Plan(mustVersions(t, "2.0.0", "1.0.0"), tc.groups)
			if got, want := stringsOf(result.Feasible), []string{"1.0.0", "2.0.0"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("got %v want %v", got, want)
			}
			if result.Lowest.String() != "1.0.0" || result.Highest.String() != "2.0.0" {
				t.Fatalf("bounds: %v .. %v", result.Lowest, result.Highest)
			}
			if result.Reason != ReasonFeasible {
				t.Fatalf("unexpected reason: %s", result.Reason)
			}
		})
	}
}

func TestPlanIgnoresNil(t *testing.T) {
	candidates := []*Version{mustVersion(t, "1.0.0"), nil}
	groups := []ConstraintGroup{
		{Name: "runtime", Constraints: Constraints{parseMustSingle(t, ">= 1.0"), nil}},
	}

	result := Plan(candidates, groups)
	if got, want := stringsOf(result.Feasible), []string{"1.0.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func parseMustSingle(t *testing.T, s string) *Constraint {
	t.Helper()

	c, err := parseSingle(s)
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	return c
}

func TestPlanStrings(t *testing.T) {
	result, err := PlanStrings(
		[]string{"1.2.0", "1.5.0", "1.5.0", "2.0.0"},
		[]ConstraintGroupExpr{
			{Name: "runtime", Expr: ">= 1.0, < 2.0"},
			{Name: "api", Expr: ">= 1.2"},
		},
	)
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if got, want := stringsOf(result.Feasible), []string{"1.2.0", "1.5.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPlanStringsRejectsInvalidInput(t *testing.T) {
	cases := []struct {
		name       string
		candidates []string
		groups     []ConstraintGroupExpr
		wantErrs   []string
	}{
		{
			name:       "bad candidate",
			candidates: []string{"1.0.0", "not-a-version"},
			wantErrs:   []string{"candidate 1", "not-a-version"},
		},
		{
			name:       "overflowing candidate segment",
			candidates: []string{"1.0.0", "1138777878078144567552950000000"},
			wantErrs:   []string{"candidate 1"},
		},
		{
			name:       "bad constraint",
			candidates: []string{"1.0.0"},
			groups:     []ConstraintGroupExpr{{Name: "runtime", Expr: ">= 1.x"}},
			wantErrs:   []string{"runtime", "malformed constraint"},
		},
		{
			name:       "empty constraint expression",
			candidates: []string{"1.0.0"},
			groups:     []ConstraintGroupExpr{{Name: "runtime", Expr: ""}},
			wantErrs:   []string{"runtime", "malformed constraint"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, err := PlanStrings(tc.candidates, tc.groups)
			if err == nil {
				t.Fatalf("expected an error, got %#v", result)
			}
			if result != nil {
				t.Fatal("result must be nil when the input is rejected")
			}
			for _, want := range tc.wantErrs {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestPlanOverflowBound(t *testing.T) {
	// A pessimistic constraint whose upper bound would overflow int64. It must
	// be treated as unbounded rather than wrapping around.
	max := fmt.Sprintf("%d", maxSegment)
	candidates := []string{max + ".0.1", max + ".0.0", max + ".0.2"}
	groups := []ConstraintGroupExpr{{Name: "runtime", Expr: "~> " + max + ".0"}}

	result, err := PlanStrings(candidates, groups)
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if result.UpperBound != nil {
		t.Fatalf("expected unbounded upper, got %s", result.UpperBound)
	}
	want := []string{max + ".0.0", max + ".0.1", max + ".0.2"}
	if got := stringsOf(result.Feasible); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestPlanConcurrent(t *testing.T) {
	candidates := mustVersions(t, "1.0.0", "1.2.0", "1.4.0", "2.0.0")
	shared := []ConstraintGroup{
		mustGroup(t, "runtime", ">= 1.0, < 2.0"),
		mustGroup(t, "api", ">= 1.1"),
	}

	const want = "1.2.0,1.4.0"

	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				result := Plan(candidates, shared)
				if got := strings.Join(stringsOf(result.Feasible), ","); got != want {
					errs <- fmt.Errorf("got %q want %q", got, want)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}

	// The shared constraints must be untouched.
	if got := shared[0].Constraints.String(); got != ">= 1.0, < 2.0" {
		t.Fatalf("constraints mutated: %s", got)
	}
	if got := shared[1].Constraints.String(); got != ">= 1.1" {
		t.Fatalf("constraints mutated: %s", got)
	}
}

func TestPlanDoesNotReturnInputSlice(t *testing.T) {
	candidates := mustVersions(t, "1.0.0")
	result := Plan(candidates, nil)
	result.Feasible[0] = mustVersion(t, "9.9.9")

	if candidates[0].String() != "1.0.0" {
		t.Fatalf("result aliases the input slice: %s", candidates[0])
	}
}

// TestPlanAgreesWithCheck is a randomized cross-check against the existing
// single-version API, using versions and constraints that exercise segment
// specificity and prerelease handling.
func TestPlanAgreesWithCheck(t *testing.T) {
	versionPool := []string{
		"0.9.9", "1.0.0", "1.0.0.0", "1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-beta",
		"1.0.0+build", "1.0.1", "1.1.0", "1.2.0-rc.1", "1.2.0", "1.2.5", "1.9.9",
		"2.0.0-alpha", "2.0.0", "2.1.0", "3.0.0",
	}
	widerPool := append([]string{}, versionPool...)
	widerPool = append(widerPool,
		"1.0.0.0", "1.0.0.0-alpha", "1.2.0.0", "1.2.0.0-rc.1", "2.0.0.0-alpha",
		"1.0.0-alpha.0", "1.0.0-zzz", "1.2.0-rc.2", "0.9.9.1", "1.2.1",
	)
	exprPool := []string{
		">= 1.0.0", "> 1.0.0", "< 2.0.0", "<= 2.0.0", "= 1.2.0", "!= 1.2.0",
		">= 1.0.0-alpha", "< 1.2.0-rc.2", "~> 1.2", "~> 1.2.0", "~> 1.0.0-alpha",
		"<= 1.0.0", ">= 1.0.0, < 2.0.0", "> 0.9, != 1.2.0",
	}

	candidates := mustVersions(t, versionPool...)
	for i, a := range exprPool {
		for j, b := range exprPool {
			if j < i {
				continue
			}

			ga := mustGroup(t, fmt.Sprintf("a%d", i), a)
			gb := mustGroup(t, fmt.Sprintf("b%d", j), b)

			result := Plan(candidates, []ConstraintGroup{ga, gb})
			if result.Reason == ReasonConflictingConstraints {
				// Every reported conflict must be real: check that no version
				// in a wide pool satisfies both groups.
				for _, raw := range widerPool {
					v := mustVersion(t, raw)
					if ga.Check(v) && gb.Check(v) {
						t.Fatalf("%q and %q reported as conflicting but %s satisfies both", a, b, v)
					}
				}
			}

			var want []*Version
			for _, v := range candidates {
				if ga.Check(v) && gb.Check(v) {
					want = append(want, v)
				}
			}
			sort.SliceStable(want, func(i, j int) bool {
				return compareForSort(want[i], want[j]) < 0
			})
			if got := stringsOf(result.Feasible); !reflect.DeepEqual(got, stringsOf(want)) {
				t.Fatalf("%q and %q: got %v want %v", a, b, got, stringsOf(want))
			}
		}
	}
}

// TestPlanConflictSoundness exhaustively combines constraints over a pool of
// versions that exercises Compare's equality quirks (extra zero segments,
// prerelease specificity) and asserts every reported conflict is real. Compare
// is not transitive across segment counts, so most interval reasoning would
// report false conflicts here.
func TestPlanConflictSoundness(t *testing.T) {
	witnesses := make([]*Version, 0, 400)
	for _, core := range []string{
		"0.9.9", "1", "1.0.0", "1.0.0.0", "1.0.0.0.0", "1.0.1", "1.2", "1.2.0",
		"1.2.3", "1.2.3.4", "2", "2.0.0", "2.0.0.0", "9.9.9",
	} {
		for _, pre := range []string{
			"", "-0", "-1", "-a", "-alpha", "-alpha.0", "-alpha.1", "-alpha.beta",
			"-rc.1", "-zzz",
		} {
			witnesses = append(witnesses, mustVersion(t, core+pre))
		}
	}

	var exprs []string
	for _, base := range []string{
		"0.9.9", "1", "1.0.0", "1.0.0.0", "1.2", "1.2.0", "1.2.3", "2", "2.0.0",
		"1.0.0-alpha", "1.0.0.0-alpha", "1.0.0-alpha.0", "1.2.3-zzz", "2.0.0-rc.1",
	} {
		for _, op := range []string{"=", "!=", ">", "<", ">=", "<=", "~>"} {
			exprs = append(exprs, op+" "+base)
		}
	}

	one := []*Version{mustVersion(t, "0.0.0")}
	for i, a := range exprs {
		for j := i; j < len(exprs); j++ {
			groups := []ConstraintGroup{
				mustGroup(t, "a", a),
				mustGroup(t, "b", exprs[j]),
			}

			result := Plan(one, groups)
			if result.Reason != ReasonConflictingConstraints {
				continue
			}

			if len(result.Conflicts) == 0 {
				t.Fatalf("%q/%q: conflicting reason without a conflict", a, exprs[j])
			}
			for _, witness := range witnesses {
				if groups[0].Check(witness) && groups[1].Check(witness) {
					t.Fatalf("%q and %q reported as conflicting, but %s satisfies both: %s",
						a, exprs[j], witness, result.Conflicts[0])
				}
			}
		}
	}
}

// TestPlanConflictDetectionThreeWay covers contradictions that no pair of
// constraints reveals.
func TestPlanConflictDetectionThreeWay(t *testing.T) {
	groups := []ConstraintGroup{
		mustGroup(t, "infra", ">= 0.1.0"),
		mustGroup(t, "runtime", ">= 1.0.0, <= 2.0.0, != 1.5.0"),
		mustGroup(t, "storage", ">= 1.5.0, <= 2.0.0"),
		mustGroup(t, "cache", ">= 1.0.0, <= 1.5.0"),
	}

	result := Plan(nil, groups)
	if result.Reason != ReasonConflictingConstraints {
		t.Fatalf("expected a conflict, got %s: %s", result.Reason, result.Detail)
	}
	if len(result.Conflicts) != 1 {
		t.Fatalf("expected one conflict, got %v", result.Conflicts)
	}

	conflict := result.Conflicts[0]
	want := []string{"runtime", "storage", "cache"}
	if !reflect.DeepEqual(conflict.Groups, want) {
		t.Fatalf("expected groups %v, got %v", want, conflict.Groups)
	}
	if conflict.Lower == nil || conflict.Lower.String() != "1.5.0" {
		t.Fatalf("expected lower bound 1.5.0, got %v", conflict.Lower)
	}
	if conflict.Upper == nil || conflict.Upper.String() != "1.5.0" {
		t.Fatalf("expected upper bound 1.5.0, got %v", conflict.Upper)
	}
	if len(conflict.Excluded) != 1 || conflict.Excluded[0].String() != "1.5.0" {
		t.Fatalf("expected 1.5.0 to be excluded, got %v", conflict.Excluded)
	}
	if !strings.Contains(conflict.String(), "runtime") || !strings.Contains(conflict.String(), "cache") {
		t.Fatalf("conflict should name the responsible groups: %s", conflict)
	}
}

// TestPlanConflictWithinOneGroup covers a group whose own constraints conflict.
func TestPlanConflictWithinOneGroup(t *testing.T) {
	group := mustGroup(t, "runtime", "= 1.0.0, > 1.0.0")

	result := Plan(mustVersions(t, "1.0.0"), []ConstraintGroup{group})
	if result.Reason != ReasonConflictingConstraints {
		t.Fatalf("expected a conflict, got %s", result.Reason)
	}

	conflict := result.Conflicts[0]
	if !reflect.DeepEqual(conflict.Groups, []string{"runtime"}) {
		t.Fatalf("expected one group, got %v", conflict.Groups)
	}
	if conflict.Lower == nil || conflict.Upper == nil ||
		conflict.Lower.String() != "1.0.0" || conflict.Upper.String() != "1.0.0" {
		t.Fatalf("expected bounds at 1.0.0, got %s .. %s", conflict.Lower, conflict.Upper)
	}
	if conflict.LowerInclusive || !conflict.UpperInclusive {
		t.Fatalf("expected exclusive lower and inclusive upper, got %+v", conflict)
	}
}

// TestPlanReportsEveryPairConflict checks that independent contradictions are
// all reported, in a deterministic order.
func TestPlanReportsEveryPairConflict(t *testing.T) {
	groups := []ConstraintGroup{
		mustGroup(t, "a", ">= 3.0.0, < 4.0.0"),
		mustGroup(t, "b", ">= 5.0.0, < 6.0.0"),
		mustGroup(t, "c", "< 2.0.0"),
	}

	result := Plan(nil, groups)
	if len(result.Conflicts) != 3 {
		t.Fatalf("expected 3 pairwise conflicts, got %d: %v", len(result.Conflicts), result.Conflicts)
	}

	for _, pair := range [][2]string{{"a", "b"}, {"a", "c"}, {"b", "c"}} {
		found := false
		for _, conflict := range result.Conflicts {
			if len(conflict.Groups) == 2 && conflict.Groups[0] == pair[0] && conflict.Groups[1] == pair[1] {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing conflict between %s and %s in %v", pair[0], pair[1], result.Conflicts)
		}
	}

	// Rendering must be deterministic.
	a, b := result.Conflicts[0].String(), result.Conflicts[0].String()
	if a != b {
		t.Fatalf("conflict string is not deterministic: %q vs %q", a, b)
	}
}

func TestPlanPlanner(t *testing.T) {
	planner := NewPlanner(
		mustGroup(t, "runtime", ">= 1.0, < 2.0"),
		mustGroup(t, "api", ">= 1.2"),
	)

	if got := len(planner.Groups()); got != 2 {
		t.Fatalf("expected 2 groups, got %d", got)
	}

	result := planner.Plan(mustVersions(t, "1.0.0", "1.2.0", "1.5.0", "2.0.0"))
	if got, want := stringsOf(result.Feasible), []string{"1.2.0", "1.5.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}

	fromStrings, err := planner.PlanStrings([]string{"1.2.0", "2.0.0"})
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if got, want := stringsOf(fromStrings.Feasible), []string{"1.2.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}

	if _, err := planner.PlanStrings([]string{"nope"}); err == nil {
		t.Fatal("expected a parse error")
	}

	// The Planner's groups are a copy and cannot be mutated by callers.
	groups := planner.Groups()
	groups[0].Name = "changed"
	if planner.Groups()[0].Name != "runtime" {
		t.Fatal("Planner.Groups did not return a copy")
	}

	var nilPlanner *Planner
	if !nilPlanner.Plan(mustVersions(t, "1.0.0")).HasFeasible() {
		t.Fatal("nil Planner should plan with no constraints")
	}
}

func TestPlanVersionsFromStrings(t *testing.T) {
	result, err := PlanVersionsFromStrings(
		[]string{"1.1.0", "1.3.0", "2.0.0"},
		ConstraintGroupSpec{Name: "runtime", Expr: ">= 1.0, < 2.0"},
	)
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if got, want := stringsOf(result.Feasible), []string{"1.1.0", "1.3.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}

	if _, err := PlanVersionsFromStrings([]string{"1.0.0"}, ConstraintGroupSpec{Name: "bad", Expr: "1.x"}); err == nil {
		t.Fatal("expected the whole input to be rejected")
	}
}

// TestPlanOverflowSegments covers constraints and candidates at the int64
// limit, where pessimistic upper bounds would overflow.
func TestPlanOverflowSegments(t *testing.T) {
	max := strconv.FormatInt(maxSegment, 10)

	result, err := PlanStrings(
		[]string{max + ".0.0", max + ".0.1", max + ".1.0"},
		[]ConstraintGroupExpr{{Name: "runtime", Expr: "~> " + max}},
	)
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if result.UpperBound != nil {
		t.Fatalf("expected no upper bound, got %s", result.UpperBound)
	}
	if !result.HasFeasible() {
		t.Fatalf("expected feasible versions: %s", result.Detail)
	}

	// A maximum-valued segment that is not the one being incremented still
	// yields a usable upper bound.
	result, err = PlanStrings(nil, []ConstraintGroupExpr{{Name: "runtime", Expr: "~> 1.2." + max}})
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if result.UpperBound == nil || result.UpperBound.String() != "1.3.0" {
		t.Fatalf("expected upper bound 1.3.0, got %v", result.UpperBound)
	}

	// "~> MAX.1" would increment the highest segment, so it is treated as
	// unbounded rather than wrapping to a negative value.
	result, err = PlanStrings(nil, []ConstraintGroupExpr{{Name: "runtime", Expr: "~> " + max + ".1"}})
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	if result.UpperBound != nil {
		t.Fatalf("expected no upper bound, got %s", result.UpperBound)
	}
}

// TestPlanZeroRangeExclusion covers an inclusive upper bound of 0.0.0 combined
// with an exclusion of it, the only release point that has no lower bound.
func TestPlanZeroRangeExclusion(t *testing.T) {
	groups := []ConstraintGroup{
		mustGroup(t, "runtime", "<= 0"),
		mustGroup(t, "blocklist", "!= 0.0.0"),
	}

	result := Plan(mustVersions(t, "0.0.0"), groups)
	if result.Reason != ReasonConflictingConstraints {
		t.Fatalf("expected a conflict, got %s (%s)", result.Reason, result.Detail)
	}
	if len(result.Conflicts) == 0 || result.Conflicts[0].Upper == nil {
		t.Fatalf("expected an upper bound, got %#v", result.Conflicts)
	}

	// The same exclusion without the upper bound leaves versions such as
	// 0.0.0-alpha, so it is not a conflict.
	result = Plan(mustVersions(t, "0.0.0"), []ConstraintGroup{
		mustGroup(t, "runtime", "< 1.0.0"),
		mustGroup(t, "blocklist", "!= 0.0.0"),
	})
	if result.Reason != ReasonNoMatchingCandidate {
		t.Fatalf("expected no matching candidate, got %s", result.Detail)
	}
}

func TestPlanVersionsAndOK(t *testing.T) {
	result := PlanVersions(mustVersions(t, "1.0.0", "2.0.0"),
		mustGroup(t, "runtime", ">= 1.5"))
	if !result.OK() || result.Reason != ReasonFeasible {
		t.Fatalf("unexpected result: %s", result.Detail)
	}
	if got, want := stringsOf(result.Feasible), []string{"2.0.0"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}

	var nilResult *PlanResult
	if nilResult.HasFeasible() || nilResult.OK() {
		t.Fatal("a nil result must not report feasibility")
	}
}

func TestMustConstraintGroupPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("expected a panic for a malformed constraint")
		}
	}()

	MustConstraintGroup("bad", ">= 1.x")
}
