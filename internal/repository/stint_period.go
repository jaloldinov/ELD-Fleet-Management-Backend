package repository

import "github.com/jaloldinov/ELD-learning/internal/model"

// stintPeriodSQL is the time filter shared by both stint histories. a is the
// stint table's alias; the parameters are $n (at), $n+1 (from), $n+2 (to).
//
// A stint runs over [started_at, ended_at), ended_at NULL = still running.
//   - running at instant T:  started_at <= T  AND (ended_at IS NULL OR ended_at > T)
//   - overlaps [F, T):       started_at <  T  AND (ended_at IS NULL OR ended_at > F)
//
// "Overlaps" catches every stint that touches the window, also one that
// started before F or is still running: a stint from Monday to Friday
// overlaps "Wednesday".
func stintPeriodSQL(a, at, from, to string) string {
	return `
		  AND (` + at + `::timestamptz IS NULL OR (` + a + `.started_at <= ` + at + `
		       AND (` + a + `.ended_at IS NULL OR ` + a + `.ended_at > ` + at + `)))
		  AND (` + to + `::timestamptz IS NULL OR ` + a + `.started_at < ` + to + `)
		  AND (` + from + `::timestamptz IS NULL OR ` + a + `.ended_at IS NULL OR ` + a + `.ended_at > ` + from + `)`
}

func periodArgs(p model.StintPeriod) []any {
	return []any{p.At, p.From, p.To}
}
