package app

import (
	"errors"
	"fmt"
	"strings"

	"github.com/paulourio/bqui/internal/bq"
	"github.com/paulourio/bqui/internal/store"
)

// ErrNoJobDoc is returned when no job document is open.
var ErrNoJobDoc = errors.New("app: no job document is open")

// JobHistoryTitle names the Job History node and job documents.
const JobHistoryTitle = "Job History"

const tooltipSQLLines = 10

// JobTooltip describes a job for its hover tooltip: the head of its SQL,
// then state, timing, bytes, statement type and where it ran.
func JobTooltip(r JobRow) string {
	var b strings.Builder

	sql := strings.TrimSpace(r.SQL)
	if sql == "" {
		sql = "(no SQL: " + strings.ToLower(r.Type) + " job)"
	}

	lines := strings.Split(sql, "\n")
	if len(lines) > tooltipSQLLines {
		lines = append(lines[:tooltipSQLLines], fmt.Sprintf("… %d more lines", len(lines)-tooltipSQLLines))
	}

	b.WriteString(strings.Join(lines, "\n"))
	b.WriteString("\n\n")

	facts := []string{strings.ToUpper(r.State[:min(1, len(r.State))]) + r.State[min(1, len(r.State)):]}
	if !r.Created.IsZero() {
		facts = append(facts, r.Created.Local().Format("2006-01-02 15:04:05"))
	}

	if r.Duration > 0 {
		facts = append(facts, r.Duration.Round(10*1e6).String())
	}

	switch {
	case r.CacheHit:
		facts = append(facts, "cached")
	case r.Processed > 0:
		facts = append(facts, Bytes(r.Processed)+" processed")
	}

	if r.Statement != "" {
		facts = append(facts, r.Statement)
	}

	b.WriteString(strings.Join(facts, " · "))
	b.WriteString("\n" + r.Ref.Project + " · " + r.Ref.Location + " · " + r.Ref.JobID)

	if r.Error != "" {
		b.WriteString("\n✕ " + r.Error)
	}

	return b.String()
}

// OpenJobDoc shows a job's SQL as a read-only document. The open query is
// saved first; the job document itself is never autosaved or persisted, so
// relaunching restores the last real query.
func (w *Workspace) OpenJobDoc(ref bq.JobRef, sql string) error {
	if w.Tab().File != "" && w.Dirty() {
		if err := w.Save(); err != nil {
			return err
		}
	}

	if err := w.Flush(); err != nil {
		w.opts.Logger.Warn("flush before job document", "err", err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	w.jobDoc = &ref
	w.tab.SQL, w.tab.File, w.tab.Title, w.tab.CursorLine, w.tab.CursorCol = sql, "", ref.JobID, 0, 0
	w.tab.Remote, w.tab.RemoteHead, w.tab.RemoteText = "", "", ""
	w.fileText, w.fileID = "", ""

	return nil
}

// JobDoc returns the job shown read-only, if a job document is open.
func (w *Workspace) JobDoc() (bq.JobRef, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.jobDoc == nil {
		return bq.JobRef{}, false
	}

	return *w.jobDoc, true
}

// SaveJobDocTo copies the job document's SQL into a new query in workbook
// and opens it (editable).
func (w *Workspace) SaveJobDocTo(workbook string) (store.Query, error) {
	ref, ok := w.JobDoc()
	if !ok {
		return store.Query{}, ErrNoJobDoc
	}

	q, err := w.opts.Library.CreateQuery(workbook, "-- From job "+ref.JobID+"\n"+w.Tab().SQL)
	if err != nil {
		return q, err
	}

	_, err = w.Open(q.Path)

	return q, err
}

func (w *Workspace) clearJobDocLocked() { w.jobDoc = nil }
