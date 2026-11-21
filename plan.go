// Copyright IBM Corp. 2014, 2026
// SPDX-License-Identifier: MPL-2.0

package version

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

// maxSegment is the largest value a version segment can hold. Segments are
// parsed with strconv.ParseInt, so this is the int64 maximum.
const maxSegment = int64(1<<63 - 1)

// ConstraintGroup is a named set of constraints. A version is feasible for a
// plan only if it satisfies every constraint in every group, using the same
// semantics as Constraints.Check. Constraints within a group are combined with
// AND, and groups are combined with AND as well.
//
// An empty group (or a nil Constraints slice) imposes no restriction.
type ConstraintGroup struct {
	Name        string
	Constraints Constraints
}

// Check reports whether v satisfies every constraint in the group.
func (g ConstraintGroup) Check(v *Version) bool {
	return g.Constraints.Check(v)
}

// String returns the group name followed by its constraints.
func (g ConstraintGroup) String() string {
	return fmt.Sprintf("%s: %s", g.Name, g.Constraints)
}

// ConstraintGroupExpr is a named constraint expression, as accepted by
// PlanStrings. Expr uses the same comma-separated syntax as NewConstraint.
type ConstraintGroupExpr struct {
	Name string
	Expr string
}

// ConstraintGroupSpec is an alias for ConstraintGroupExpr.
type ConstraintGroupSpec = ConstraintGroupExpr

// NewConstraintGroup parses expr with NewConstraint and returns a named group.
func NewConstraintGroup(name, expr string) (ConstraintGroup, error) {
	constraints, err := NewConstraint(expr)
	if err != nil {
		return ConstraintGroup{}, fmt.Errorf("constraint group %q: %w", name, err)
	}

	return ConstraintGroup{Name: name, Constraints: constraints}, nil
}

// MustConstraintGroup is a helper that wraps a call to NewConstraintGroup and
// panics if the error is non-nil.
func MustConstraintGroup(name, expr string) ConstraintGroup {
	group, err := NewConstraintGroup(name, expr)
	if err != nil {
		panic(err)
	}

	return group
}

// NewConstraintGroups parses several named expressions. Parsing is
// all-or-nothing: the first malformed expression rejects the whole input and
// no groups are returned.
func NewConstraintGroups(exprs ...ConstraintGroupExpr) ([]ConstraintGroup, error) {
	groups := make([]ConstraintGroup, len(exprs))
	for i, expr := range exprs {
		g, err := NewConstraintGroup(expr.Name, expr.Expr)
		if err != nil {
			return nil, fmt.Errorf("constraint group %d: %w", i, err)
		}
		groups[i] = g
	}

	return groups, nil
}

// PlanReason classifies the outcome of a plan.
type PlanReason string

const (
	// ReasonFeasible means at least one candidate satisfies every group.
	ReasonFeasible PlanReason = "feasible"
	// ReasonNoCandidates means no candidate version was supplied and the
	// constraints are not contradictory. Conflicts may still describe the
	// bounds the constraints require. When the groups themselves contradict
	// each other, ReasonConflictingConstraints takes precedence, because no
	// candidate set could satisfy them.
	ReasonNoCandidates PlanReason = "no-candidates"
	// ReasonConflictingConstraints means the groups cannot all be satisfied by
	// any version. Conflicts reports at least one contradictory set.
	ReasonConflictingConstraints PlanReason = "conflicting-constraints"
	// ReasonNoMatchingCandidate means the groups can be satisfied, but none of
	// the supplied candidates falls inside the region they allow. Conflicts
	// describes the required bounds and the supplied candidate span.
	ReasonNoMatchingCandidate PlanReason = "no-matching-candidate"
)

// Conflict describes a set of constraints that cannot all be satisfied,
// together with the boundary that makes them contradictory. A plan that fails
// only because no candidate matched also reports a Conflict, carrying the
// bounds the constraints required; see Detail and PlanResult.Reason to tell
// the two cases apart.
type Conflict struct {
	// Groups lists the distinct group names involved, in order of appearance.
	Groups []string

	// Constraints holds the constraints that established the reported bounds.
	Constraints []*Constraint

	// Lower and Upper are the tightest bounds implied by the constraints.
	// Either may be nil when that side is unbounded. LowerInclusive and
	// UpperInclusive report whether the boundary value itself is accepted.
	Lower          *Version
	LowerInclusive bool
	Upper          *Version
	UpperInclusive bool

	// Excluded lists versions ruled out by "!=" constraints involved in the
	// conflict.
	Excluded []*Version

	// Detail is a human-readable explanation of the conflict. When the
	// constraints are satisfiable but no candidate matched (Reason is
	// ReasonNoMatchingCandidate), Detail describes the range that was
	// required and the span of the supplied candidates rather than a
	// contradiction.
	Detail string
}

// String returns a deterministic description of the conflict.
func (c Conflict) String() string {
	var b strings.Builder

	switch len(c.Groups) {
	case 0:
		b.WriteString("constraint conflict")
	case 1:
		fmt.Fprintf(&b, "constraint conflict in group %q", c.Groups[0])
	default:
		quoted := make([]string, len(c.Groups))
		for i, name := range c.Groups {
			quoted[i] = fmt.Sprintf("%q", name)
		}
		fmt.Fprintf(&b, "constraint conflict between groups %s", strings.Join(quoted, ", "))
	}

	if len(c.Constraints) > 0 {
		quoted := make([]string, len(c.Constraints))
		for i, con := range c.Constraints {
			quoted[i] = fmt.Sprintf("%q", con.String())
		}
		fmt.Fprintf(&b, ": %s", strings.Join(quoted, ", "))
	}

	if c.Lower != nil {
		fmt.Fprintf(&b, "; lower bound %s", c.Lower)
		if !c.LowerInclusive {
			b.WriteString(" (exclusive)")
		}
	}
	if c.Upper != nil {
		fmt.Fprintf(&b, "; upper bound %s", c.Upper)
		if !c.UpperInclusive {
			b.WriteString(" (exclusive)")
		}
	}
	if len(c.Excluded) > 0 {
		excluded := make([]string, len(c.Excluded))
		for i, v := range c.Excluded {
			excluded[i] = v.String()
		}
		fmt.Fprintf(&b, "; excluding %s", strings.Join(excluded, ", "))
	}
	if c.Detail != "" {
		fmt.Fprintf(&b, "; %s", c.Detail)
	}

	return b.String()
}

// PlanResult is the result of planning candidate versions against named
// constraint groups.
//
// Feasible contains the candidates that satisfy every group, sorted in
// ascending version order. It is never nil, even when no candidate is
// feasible. Candidates whose canonical String is identical are collapsed, so
// repeated input does not create repeated results. Versions that compare equal
// but have different metadata (for example "1.0.0+alpha" and "1.0.0+beta") are
// both kept, and ties are ordered by canonical String so the output order does
// not depend on the input order.
type PlanResult struct {
	// Feasible is the sorted, de-duplicated set of accepted candidates.
	Feasible []*Version

	// Lowest and Highest are the first and last entries of Feasible. Both are
	// nil when Feasible is empty.
	Lowest  *Version
	Highest *Version

	// Reason classifies the outcome.
	Reason PlanReason

	// Detail explains the outcome in human-readable terms. It is empty when
	// Reason is ReasonFeasible.
	Detail string

	// Conflicts explains why planning failed. It is empty when the plan
	// succeeded. When the constraints themselves are contradictory, every
	// reported conflict is a set of constraints that no version can satisfy
	// together, with the bounds at which they collide. When the constraints are
	// satisfiable but no candidate fits, a single conflict describes the
	// required bounds and the supplied candidate span.
	Conflicts []Conflict

	// LowerBound, LowerBoundInclusive, UpperBound and UpperBoundInclusive are
	// the tightest bounds implied by all of the constraints, for diagnostics.
	// LowerBound or UpperBound is nil when that side is unbounded.
	//
	// These are comparison bounds only: an equality constraint accepts every
	// version that compares equal to it, including versions with extra zero
	// segments, so they are not an exact description of the accepted set.
	LowerBound          *Version
	LowerBoundInclusive bool
	UpperBound          *Version
	UpperBoundInclusive bool

	// CandidateLow and CandidateHigh bound the supplied candidate pool,
	// ignoring constraints. Both are nil when no candidate was supplied.
	CandidateLow  *Version
	CandidateHigh *Version
}

// HasFeasible reports whether at least one candidate satisfies all groups.
func (r *PlanResult) HasFeasible() bool {
	return r != nil && len(r.Feasible) > 0
}

// OK reports whether the plan found at least one feasible version. It is an
// alias for HasFeasible.
func (r *PlanResult) OK() bool {
	return r.HasFeasible()
}

// Plan selects the candidate versions satisfying every named constraint group.
//
// Prerelease, prefix and metadata handling is delegated to the existing
// Constraint.Check and Version.Compare implementations, so planning agrees with
// single-version checks: a comparison constraint without a prerelease never
// accepts a prerelease, metadata does not affect ordering, and equality keeps
// its relaxed behavior for versions that compare equal.
//
// Duplicate candidates are collapsed by their canonical String; the first
// occurrence is retained. The returned Feasible slice is sorted ascending, with
// ties between otherwise equal versions broken by canonical String, so the
// output does not depend on input order.
//
// When no candidate is feasible, Reason and Detail explain why. If the
// constraints are contradictory, Conflicts names at least one contradictory
// set with its bounds. If the constraints are satisfiable but no candidate
// falls in range, Reason is ReasonNoMatchingCandidate and Conflicts describes
// the required bounds and the supplied candidate span.
//
// Empty candidates produce an empty, non-nil Feasible slice and Reason
// ReasonNoCandidates (or ReasonConflictingConstraints when the groups are
// contradictory). Empty groups impose no restriction. Version strings whose
// numeric segments overflow int64 are rejected by PlanStrings.
//
// Plan never mutates the candidates, groups, or constraints. It reads only
// immutable state, so it is safe for concurrent use as long as the caller does
// not mutate its arguments concurrently. Nil candidates and nil constraints
// are ignored.
func Plan(candidates []*Version, groups []ConstraintGroup) *PlanResult {
	result := &PlanResult{Feasible: []*Version{}}

	pool := dedupeCandidates(candidates)
	if len(pool) > 0 {
		result.CandidateLow = pool[0]
		result.CandidateHigh = pool[len(pool)-1]
	}

	refs := collectRefs(groups)
	result.LowerBound, result.LowerBoundInclusive,
		result.UpperBound, result.UpperBoundInclusive, _, _ = boundsFor(refs)

	for _, v := range pool {
		if satisfiesAll(v, groups) {
			result.Feasible = append(result.Feasible, v)
		}
	}

	if len(result.Feasible) > 0 {
		result.Lowest = result.Feasible[0]
		result.Highest = result.Feasible[len(result.Feasible)-1]
		result.Reason = ReasonFeasible
		return result
	}

	result.Conflicts = structuralConflicts(refs)

	switch {
	case len(result.Conflicts) > 0:
		// Contradictory constraints can never be satisfied, with or without
		// candidates, so they are the primary explanation for the failure.
		result.Reason = ReasonConflictingConstraints
		result.Detail = result.Conflicts[0].String()
	case len(pool) == 0:
		result.Reason = ReasonNoCandidates
		result.Detail = "no candidate versions were supplied"
		if len(refs) > 0 {
			// Report the bounds the constraints require even though there was
			// nothing to test them against.
			result.Conflicts = []Conflict{candidateConflict(result, refs, true)}
		}
	default:
		result.Reason = ReasonNoMatchingCandidate
		result.Conflicts = []Conflict{candidateConflict(result, refs, false)}
		result.Detail = result.Conflicts[0].String()
	}

	return result
}

// PlanVersions is a convenience wrapper around Plan with variadic groups.
func PlanVersions(candidates []*Version, groups ...ConstraintGroup) *PlanResult {
	return Plan(candidates, groups)
}

// PlanStrings parses candidates and named constraint expressions and then
// calls Plan.
//
// The whole input is rejected if any candidate or any expression fails to
// parse; in that case the returned result is nil and the error identifies the
// offending input. An empty expression is a parse error, matching
// NewConstraint. Use an empty groups slice to impose no restriction.
func PlanStrings(candidates []string, groups []ConstraintGroupExpr) (*PlanResult, error) {
	parsedCandidates, err := parseCandidates(candidates)
	if err != nil {
		return nil, err
	}

	parsedGroups, err := NewConstraintGroups(groups...)
	if err != nil {
		return nil, fmt.Errorf("plan: %w", err)
	}

	return Plan(parsedCandidates, parsedGroups), nil
}

// PlanVersionsFromStrings is a convenience wrapper around PlanStrings with
// variadic group specifications.
func PlanVersionsFromStrings(candidates []string, specs ...ConstraintGroupSpec) (*PlanResult, error) {
	return PlanStrings(candidates, specs)
}

func parseCandidates(candidates []string) ([]*Version, error) {
	parsed := make([]*Version, len(candidates))
	for i, raw := range candidates {
		v, err := NewVersion(raw)
		if err != nil {
			return nil, fmt.Errorf("plan: candidate %d %q: %w", i, raw, err)
		}
		parsed[i] = v
	}

	return parsed, nil
}

// Planner plans candidates against a fixed set of constraint groups. It is
// safe for concurrent use: planning only reads the groups.
type Planner struct {
	groups []ConstraintGroup
}

// NewPlanner returns a Planner for the given groups. The groups and their
// constraint slices are copied, so later changes to the caller's slices do not
// affect the Planner. The constraints themselves are immutable.
func NewPlanner(groups ...ConstraintGroup) *Planner {
	copied := make([]ConstraintGroup, len(groups))
	for i, group := range groups {
		constraints := make(Constraints, len(group.Constraints))
		copy(constraints, group.Constraints)
		copied[i] = ConstraintGroup{Name: group.Name, Constraints: constraints}
	}

	return &Planner{groups: copied}
}

// Groups returns a copy of the Planner's groups.
func (p *Planner) Groups() []ConstraintGroup {
	if p == nil {
		return nil
	}

	copied := make([]ConstraintGroup, len(p.groups))
	for i, group := range p.groups {
		constraints := make(Constraints, len(group.Constraints))
		copy(constraints, group.Constraints)
		copied[i] = ConstraintGroup{Name: group.Name, Constraints: constraints}
	}

	return copied
}

// Plan plans candidates against the Planner's groups.
func (p *Planner) Plan(candidates []*Version) *PlanResult {
	if p == nil {
		return Plan(candidates, nil)
	}

	return Plan(candidates, p.groups)
}

// PlanStrings parses candidates and plans them against the Planner's groups.
func (p *Planner) PlanStrings(candidates []string) (*PlanResult, error) {
	if p == nil {
		return PlanStrings(candidates, nil)
	}

	parsed, err := parseCandidates(candidates)
	if err != nil {
		return nil, err
	}

	return Plan(parsed, p.groups), nil
}

// compareForSort imposes a deterministic total order on versions so that
// planning output does not depend on the order of the input slice.
//
// It refines Compare rather than replacing it. Compare is not transitive across
// versions with different segment counts ("1.0.0", "1.0.0.0" and
// "1.0.0-alpha" all compare equal to a neighbor but not to each other), so a
// sort driven by Compare alone could order the same input differently depending
// on how it was arranged. compareForSort first compares the segment vectors,
// then applies the same prerelease rules as Compare, and finally falls back to
// the canonical string. It only ever refines cases that Compare reports as
// equal, and agrees with Compare's sign whenever Compare is decisive.
func compareForSort(a, b *Version) int {
	if cmp := compareVersionVectors(a, b); cmp != 0 {
		return cmp
	}

	switch aPre, bPre := a.Prerelease(), b.Prerelease(); {
	case aPre == bPre:
	case aPre == "":
		return 1
	case bPre == "":
		return -1
	default:
		if cmp := comparePrereleases(aPre, bPre); cmp != 0 {
			return cmp
		}
	}

	switch {
	case a.String() < b.String():
		return -1
	case a.String() > b.String():
		return 1
	}

	return 0
}

// dedupeCandidates drops nil candidates and repeats of the same canonical
// version string, keeping the first occurrence, then sorts the result.
func dedupeCandidates(candidates []*Version) []*Version {
	seen := make(map[string]struct{}, len(candidates))
	pool := make([]*Version, 0, len(candidates))
	for _, v := range candidates {
		if v == nil {
			continue
		}
		key := v.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		pool = append(pool, v)
	}

	sort.SliceStable(pool, func(i, j int) bool {
		return compareForSort(pool[i], pool[j]) < 0
	})

	return pool
}

func satisfiesAll(v *Version, groups []ConstraintGroup) bool {
	for _, g := range groups {
		for _, c := range g.Constraints {
			if c == nil {
				continue
			}
			if !c.Check(v) {
				return false
			}
		}
	}

	return true
}

//-------------------------------------------------------------------
// Bounds
//-------------------------------------------------------------------

// constraintDomain is a comparison-range view of one constraint, used for
// bounds and conflict diagnostics.
//
// Bounds are compared as segment vectors (see segmentVector), so versions that
// differ only by trailing zero segments compare equal, exactly as Compare
// treats them when the segment counts differ. The domain is always an
// over-approximation of the versions the constraint accepts: where prerelease
// handling makes the exact set hard to describe, the domain is widened. A
// contradiction between two over-approximations is therefore a real
// contradiction, never a false one.
type constraintDomain struct {
	lower          *Version
	lowerInclusive bool
	upper          *Version
	upperInclusive bool
}

func domainFor(c *Constraint) constraintDomain {
	v := c.check
	d := constraintDomain{}

	switch c.op {
	case equal:
		d.lower, d.lowerInclusive = v, true
		d.upper, d.upperInclusive = v, true
	case notEqual:
		// An inequality only removes one version; it imposes no bound.
	case greaterThan, greaterThanEqual:
		d.lower, d.lowerInclusive = v, true
		if v.Prerelease() == "" {
			// A release comparison bound rejects every prerelease.
			d.lowerInclusive = c.op == greaterThanEqual
		}
	case lessThan, lessThanEqual:
		d.upper, d.upperInclusive = v, true
		if v.Prerelease() == "" {
			// A release comparison bound rejects every prerelease.
			d.upperInclusive = c.op == lessThanEqual
		}
	case pessimistic:
		d.lower, d.lowerInclusive = v, true
		if v.Prerelease() == "" {
			// constraintPessimistic rejects prereleases for a release bound.
			d.upper, d.upperInclusive = pessimisticUpper(v), false
		} else {
			// A prerelease pessimistic constraint accepts prereleases of the
			// same segments only, which is a subset of the point vector, and
			// rejects every release. A point is a safe over-approximation.
			d.upper, d.upperInclusive = v, true
		}
	}

	return d
}

// pessimisticUpper returns the exclusive upper bound of a release pessimistic
// constraint, or nil when the constraint has no upper bound.
func pessimisticUpper(c *Version) *Version {
	if c.si < 2 {
		// Matches constraintPessimistic: a single-segment constraint only
		// requires v >= c.
		return nil
	}

	segments := c.Segments64()
	idx := c.si - 2
	if idx < 0 || idx >= len(segments) {
		return nil
	}
	if segments[idx] == maxSegment {
		// The next version cannot be represented as an int64 segment. Treat
		// the upper bound as unbounded instead of overflowing.
		return nil
	}
	segments[idx]++
	for i := idx + 1; i < len(segments); i++ {
		segments[i] = 0
	}

	return makeVersion(segments, "")
}

// makeVersion builds a Version from segments and a prerelease. It is used for
// synthetic bounds, never for user input.
func makeVersion(segments []int64, pre string) *Version {
	cp := make([]int64, len(segments), len(segments)+3)
	copy(cp, segments)
	for len(cp) < 3 {
		cp = append(cp, 0)
	}

	return &Version{segments: cp, si: len(cp), pre: pre}
}

// segmentVector returns segments with trailing zeroes removed. Two release
// versions have the same vector exactly when Compare returns 0 for them.
func segmentVector(v *Version) []int64 {
	segments := v.Segments64()
	for len(segments) > 1 && segments[len(segments)-1] == 0 {
		segments = segments[:len(segments)-1]
	}

	return segments
}

// compareVectors compares two segment vectors lexicographically, treating a
// missing segment as zero. For two release versions, its sign matches Compare.
func compareVectors(a, b []int64) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	for _, s := range a[n:] {
		if s != 0 {
			return 1
		}
	}
	for _, s := range b[n:] {
		if s != 0 {
			return -1
		}
	}

	return 0
}

func compareVersionVectors(a, b *Version) int {
	return compareVectors(segmentVector(a), segmentVector(b))
}

type constraintRef struct {
	group string
	c     *Constraint
}

func collectRefs(groups []ConstraintGroup) []constraintRef {
	var refs []constraintRef
	for _, g := range groups {
		for _, c := range g.Constraints {
			if c == nil {
				continue
			}
			refs = append(refs, constraintRef{group: g.Name, c: c})
		}
	}

	return refs
}

// boundsFor returns the strongest lower and upper bounds over refs, together
// with the constraint that supplied each one (nil when that side is
// unbounded).
func boundsFor(refs []constraintRef) (lower *Version, lowerInclusive bool, upper *Version, upperInclusive bool, lowerRef, upperRef *constraintRef) {
	for i := range refs {
		ref := &refs[i]
		d := domainFor(ref.c)

		if d.lower != nil {
			if lower == nil {
				lower, lowerInclusive, lowerRef = d.lower, d.lowerInclusive, ref
			} else {
				switch cmp := compareVersionVectors(d.lower, lower); {
				case cmp > 0:
					lower, lowerInclusive, lowerRef = d.lower, d.lowerInclusive, ref
				case cmp == 0 && lowerInclusive && !d.lowerInclusive:
					lower, lowerInclusive, lowerRef = d.lower, false, ref
				}
			}
		}

		if d.upper != nil {
			if upper == nil {
				upper, upperInclusive, upperRef = d.upper, d.upperInclusive, ref
			} else {
				switch cmp := compareVersionVectors(d.upper, upper); {
				case cmp < 0:
					upper, upperInclusive, upperRef = d.upper, d.upperInclusive, ref
				case cmp == 0 && upperInclusive && !d.upperInclusive:
					upper, upperInclusive, upperRef = d.upper, false, ref
				}
			}
		}
	}

	return lower, lowerInclusive, upper, upperInclusive, lowerRef, upperRef
}

// prereleaseStyle reports whether a constraint can accept a version with no
// prerelease (release) and one with a prerelease (pre). Equality and inequality
// accept both, because Compare reports equal for versions whose segment counts
// differ, so "= 1.0.0" also matches "1.0.0.0-alpha". A comparison or pessimistic
// constraint on a release always rejects prereleases through prereleaseCheck,
// while a prerelease comparison bound accepts releases beyond it. Only a
// prerelease pessimistic constraint is limited to prereleases.
func prereleaseStyle(c *Constraint) (release, pre bool) {
	switch c.op {
	case equal, notEqual:
		return true, true
	case pessimistic:
		if c.check.Prerelease() != "" {
			return false, true
		}
		return true, false
	default:
		if c.check.Prerelease() != "" {
			return true, true
		}
		return true, false
	}
}

//-------------------------------------------------------------------
// Conflict detection
//-------------------------------------------------------------------

// structuralConflicts returns sets of constraints that provably cannot all be
// satisfied. Only rules that do not assume Compare is transitive are used, so
// every reported conflict is real. A satisfiable set that this analysis does
// not recognize is reported as a candidate-range failure instead.
func structuralConflicts(refs []constraintRef) []Conflict {
	conflicts := make([]Conflict, 0)
	seen := make(map[string]struct{})

	add := func(members []constraintRef, detail string) {
		members = dedupeRefs(members)
		keys := make([]string, len(members))
		for i, m := range members {
			keys[i] = m.group + "\x00" + constraintKey(m.c)
		}
		sort.Strings(keys)
		key := strings.Join(keys, "\x01")
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}

		conflicts = append(conflicts, newConflict(members, detail))
	}

	for _, ref := range refs {
		if detail, ok := emptyConstraint(ref.c); ok {
			add([]constraintRef{ref}, detail)
		}
	}

	for i := 0; i < len(refs); i++ {
		for j := i + 1; j < len(refs); j++ {
			if detail, ok := pairConflict(refs[i], refs[j]); ok {
				add([]constraintRef{refs[i], refs[j]}, detail)
			}
		}
	}

	// A range can be narrowed to a single version by three or more
	// constraints and then excluded, which no pair reveals.
	if detail, members, ok := pointConflict(refs); ok {
		add(members, detail)
	}

	return conflicts
}

// emptyConstraint reports whether a constraint cannot be satisfied by any
// version on its own.
func emptyConstraint(c *Constraint) (string, bool) {
	// Every release version has non-negative segments, so no release sorts
	// below 0.0.0, and a release comparison never accepts a prerelease.
	if c.op == lessThan && c.check.Prerelease() == "" {
		if vec := segmentVector(c.check); len(vec) == 1 && vec[0] == 0 {
			return "no version is less than 0.0.0", true
		}
	}

	return "", false
}

// pairConflict reports whether two constraints cannot both be satisfied. Each
// constraint's domain is an over-approximation of the versions it accepts, so
// empty domain intersections are real conflicts.
func pairConflict(a, b constraintRef) (string, bool) {
	aRelease, aPre := prereleaseStyle(a.c)
	bRelease, bPre := prereleaseStyle(b.c)
	if (aPre && !aRelease && !bPre) || (bPre && !bRelease && !aPre) {
		return "one constraint accepts only prerelease versions while the other rejects every prerelease", true
	}

	da, db := domainFor(a.c), domainFor(b.c)
	lower, lowerInclusive := tighterLower(da.lower, da.lowerInclusive, db.lower, db.lowerInclusive)
	upper, upperInclusive := tighterUpper(da.upper, da.upperInclusive, db.upper, db.upperInclusive)

	if lower == nil && upper != nil && upperInclusive && upper.Prerelease() == "" && isZeroVector(upper) {
		// A release upper bound rejects every prerelease, and no release is
		// below 0.0.0, so only releases comparing equal to 0.0.0 remain. An
		// inequality at that vector removes all of them.
		if exclusionMatches(a.c, upper) || exclusionMatches(b.c, upper) {
			return "the only version in range is excluded by an inequality constraint", true
		}
	}

	if lower == nil || upper == nil {
		return "", false
	}

	switch cmp := compareVersionVectors(lower, upper); {
	case cmp > 0:
		return "the lower bound is greater than the upper bound", true
	case cmp < 0:
		return "", false
	}

	// Both domains collapse to the same segment vector. Which versions remain
	// depends on their segment count and prerelease, so ask the constraint
	// functions directly over the finite set of relevant representatives.
	if pointSatisfiable(lower, []*Constraint{a.c, b.c}) {
		return "", false
	}
	if !lowerInclusive || !upperInclusive {
		return "the bounds exclude every version with the only possible segments", true
	}

	return "no version satisfies both constraints at the only possible segment values", true
}

// pointConflict reports contradictions that appear once the combined bounds
// collapse to a single segment vector, such as a point narrowed by several
// constraints and then excluded.
func pointConflict(refs []constraintRef) (string, []constraintRef, bool) {
	if len(refs) < 3 {
		return "", nil, false
	}

	lower, lowerInclusive, upper, upperInclusive, lowerRef, upperRef := boundsFor(refs)
	if lower == nil || upper == nil || compareVersionVectors(lower, upper) != 0 {
		return "", nil, false
	}

	all := make([]*Constraint, len(refs))
	for i, ref := range refs {
		all[i] = ref.c
	}
	if pointSatisfiable(lower, all) {
		return "", nil, false
	}

	// Report the constraints that establish the conflicting point plus any
	// exclusion of it.
	var excluded *constraintRef
	for i := range refs {
		if refs[i].c.op == notEqual && compareVersionVectors(refs[i].c.check, lower) == 0 {
			excluded = &refs[i]
			break
		}
	}

	members := []constraintRef{*lowerRef, *upperRef}
	if excluded != nil {
		members = append(members, *excluded)
	}

	if !lowerInclusive || !upperInclusive {
		return "the bounds exclude every version with the only possible segments", orderRefs(refs, members...), true
	}
	if excluded != nil {
		return "the only version with these segments is excluded by an inequality constraint", orderRefs(refs, members...), true
	}

	return "no version satisfies all constraints at the only possible segment values", orderRefs(refs, members...), true
}

// exclusionMatches reports whether c is an inequality that rejects every
// release version comparing equal to v.
func isZeroVector(v *Version) bool {
	vec := segmentVector(v)
	return len(vec) == 1 && vec[0] == 0
}

// exclusionMatches reports whether c is an inequality that rejects every
// release version comparing equal to v.
func exclusionMatches(c *Constraint, v *Version) bool {
	return c.op == notEqual &&
		c.check.Prerelease() == "" &&
		compareVersionVectors(c.check, v) == 0
}

// pointSatisfiable reports whether any version satisfying the combined bounds
// at point exists. Since every accepted version has the point's segment vector,
// only two things can distinguish versions: how many segments they have and
// their prerelease. Compare ignores the prerelease when the segment counts
// differ and ignores trailing zero segments, so a finite set of representatives
// covers every possible outcome.
func pointSatisfiable(point *Version, constraints []*Constraint) bool {
	return anySatisfies(pointWitnesses(point, constraints), constraints...)
}

func pointWitnesses(point *Version, constraints []*Constraint) []*Version {
	vec := segmentVector(point)

	lengths := map[int]struct{}{}
	prereleases := map[string]struct{}{"": {}, "0": {}}
	maxLength := len(vec)

	for _, c := range constraints {
		if c == nil {
			continue
		}
		n := len(c.check.segments)
		lengths[n] = struct{}{}
		if n > maxLength {
			maxLength = n
		}
		if pre := c.check.Prerelease(); pre != "" {
			prereleases[pre] = struct{}{}
		}
	}
	// A length that no constraint uses behaves the same as any other such
	// length, because Compare only uses segment count equality.
	lengths[maxLength+1] = struct{}{}
	lengths[len(vec)] = struct{}{}

	// Add a prerelease strictly between each adjacent pair of known values, and
	// one beyond the largest. Appending ".0" to a non-empty prerelease always
	// sorts above it; the release ("") is already the minimum.
	known := make([]string, 0, len(prereleases))
	for pre := range prereleases {
		known = append(known, pre)
	}
	sort.Slice(known, func(i, j int) bool {
		return comparePrereleases(known[i], known[j]) < 0
	})
	aboveMax := ""
	for _, pre := range known {
		if pre == "" {
			continue
		}
		prereleases[pre+".0"] = struct{}{}
		if aboveMax == "" || comparePrereleases(pre+".0", aboveMax) > 0 {
			aboveMax = pre + ".0"
		}
	}
	if aboveMax != "" {
		prereleases[aboveMax+".0"] = struct{}{}
	}

	var witnesses []*Version
	seen := make(map[string]struct{})
	for _, n := range sortedInts(lengths) {
		segments := make([]int64, n)
		copy(segments, vec)
		for _, pre := range sortedStrings(prereleases) {
			v := makeVersion(segments, pre)
			key := v.String()
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			witnesses = append(witnesses, v)
		}
	}

	return witnesses
}

func anySatisfies(witnesses []*Version, constraints ...*Constraint) bool {
	for _, v := range witnesses {
		ok := true
		for _, c := range constraints {
			if c != nil && !c.Check(v) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}

	return false
}

func sortedStrings(set map[string]struct{}) []string {
	values := make([]string, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)

	return values
}

func sortedInts(set map[int]struct{}) []int {
	values := make([]int, 0, len(set))
	for value := range set {
		values = append(values, value)
	}
	sort.Ints(values)

	return values
}

// orderRefs returns the given members in the order they first appear in refs,
// so a conflict lists its groups in the caller's group order.
func orderRefs(refs []constraintRef, members ...constraintRef) []constraintRef {
	include := make(map[*Constraint]struct{}, len(members))
	for _, m := range members {
		include[m.c] = struct{}{}
	}

	ordered := make([]constraintRef, 0, len(members))
	for _, ref := range refs {
		if _, ok := include[ref.c]; !ok {
			continue
		}
		delete(include, ref.c)
		ordered = append(ordered, ref)
	}

	return ordered
}

func tighterLower(a *Version, aInclusive bool, b *Version, bInclusive bool) (*Version, bool) {
	if a == nil {
		return b, bInclusive
	}
	if b == nil {
		return a, aInclusive
	}
	switch cmp := compareVersionVectors(a, b); {
	case cmp > 0:
		return a, aInclusive
	case cmp < 0:
		return b, bInclusive
	case aInclusive && !bInclusive:
		return b, false
	}

	return a, aInclusive
}

func tighterUpper(a *Version, aInclusive bool, b *Version, bInclusive bool) (*Version, bool) {
	if a == nil {
		return b, bInclusive
	}
	if b == nil {
		return a, aInclusive
	}
	switch cmp := compareVersionVectors(a, b); {
	case cmp < 0:
		return a, aInclusive
	case cmp > 0:
		return b, bInclusive
	case aInclusive && !bInclusive:
		return b, false
	}

	return a, aInclusive
}

func newConflict(members []constraintRef, detail string) Conflict {
	lower, lowerInclusive, upper, upperInclusive, _, _ := boundsFor(members)

	conflict := Conflict{
		Lower:          lower,
		LowerInclusive: lowerInclusive,
		Upper:          upper,
		UpperInclusive: upperInclusive,
		Detail:         detail,
	}

	groupSeen := make(map[string]struct{}, len(members))
	constraintSeen := make(map[string]struct{}, len(members))
	excludedSeen := make(map[string]struct{}, len(members))
	for _, m := range members {
		if m.group != "" {
			if _, ok := groupSeen[m.group]; !ok {
				groupSeen[m.group] = struct{}{}
				conflict.Groups = append(conflict.Groups, m.group)
			}
		}

		key := constraintKey(m.c)
		if _, ok := constraintSeen[key]; !ok {
			constraintSeen[key] = struct{}{}
			conflict.Constraints = append(conflict.Constraints, m.c)
		}

		if m.c.op == notEqual {
			excludedKey := m.c.check.String()
			if _, ok := excludedSeen[excludedKey]; !ok {
				excludedSeen[excludedKey] = struct{}{}
				conflict.Excluded = append(conflict.Excluded, m.c.check)
			}
		}
	}

	return conflict
}

// candidateConflict explains that no supplied candidate satisfies the
// constraints, reporting the required bounds and the candidate span. It does
// not claim the constraints are contradictory.
func candidateConflict(result *PlanResult, refs []constraintRef, noCandidates bool) Conflict {
	conflict := newConflict(refs, "no candidate satisfies the constraints")

	switch {
	case noCandidates:
		conflict.Detail = "no candidate versions were supplied; the reported bounds are required by the constraints"
	case result.CandidateLow != nil:
		conflict.Detail = fmt.Sprintf(
			"no candidate satisfies the constraints; candidates span %s to %s",
			result.CandidateLow, result.CandidateHigh)
	}

	return conflict
}

func dedupeRefs(refs []constraintRef) []constraintRef {
	seen := make(map[string]struct{}, len(refs))
	result := make([]constraintRef, 0, len(refs))
	for _, ref := range refs {
		key := ref.group + "\x00" + constraintKey(ref.c)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, ref)
	}

	return result
}

// constraintKey returns a stable key for constraints that Constraint.Equals
// considers equivalent.
func constraintKey(c *Constraint) string {
	segments := segmentVector(c.check)

	var b strings.Builder
	b.WriteString(string(c.op))
	b.WriteByte('\x00')
	for i, s := range segments {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.FormatInt(s, 10))
	}
	b.WriteByte('-')
	b.WriteString(c.check.Prerelease())

	return b.String()
}
