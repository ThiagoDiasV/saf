package web

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"saf/internal/auth"
	"saf/internal/form"
	"saf/internal/stock"
)

const historyPageSize = 10

// historyForm is the filter form as typed (and as it goes back into the inputs).
type historyForm struct {
	From, To, Type, Medicine, Lot, PatientName string
}

type historyView struct {
	Form       historyForm
	Rows       []historyRow
	Total      int
	Pager      []pagerLink
	CSVURL     string
	Medicines  []stock.Medicine
	EntryTypes []string
	ExitTypes  []string
}

type pagerLink struct {
	Label    string
	URL      string
	Current  bool
	Disabled bool
}

// historyRow is a HistoryItem with what the list shows already worded.
type historyRow struct {
	stock.HistoryItem
	When      string // movement date, with the time when it was recorded the same day
	Recorded  string // "registrado …" when backdated
	Label     string // "Entrada", "Saída", "Estorno", "Ajuste"
	Icon      string
	Subtitle  string // the type, "da saída de 01/10/2026", "Lote", "Medicamento"
	First     string // first item, or the Ajuste's subject
	More      string // second item, "e mais N medicamentos", or the Ajuste's changes
	NoteShort string
	UserFirst string
}

var kindLabels = map[string]struct{ label, icon string }{
	"in": {"Entrada", "in"}, "out": {"Saída", "out"}, "reversal": {"Estorno", "undo"}, "adjust": {"Ajuste", "edit"},
}

// historyFilter reads the filter form. The first visit shows the last 30 days.
func (s *server) historyFilter(r *http.Request) (stock.HistoryFilter, historyForm) {
	q := r.URL.Query()
	fm := historyForm{From: q.Get("de"), To: q.Get("ate"), Type: q.Get("tipo"),
		Medicine: q.Get("medicamento"), Lot: q.Get("lote"), PatientName: q.Get("paciente")}
	if r.URL.RawQuery == "" {
		today := s.now().In(stock.TimeZone)
		fm.From, fm.To = today.AddDate(0, 0, -30).Format("2006-01-02"), today.Format("2006-01-02")
	}
	f := stock.HistoryFilter{From: validDate(fm.From), To: validDate(fm.To),
		Medicine: fm.Medicine, Lot: fm.Lot, PatientName: fm.PatientName}
	kind, typ, _ := strings.Cut(fm.Type, ":")
	switch {
	case kind == "in" && (typ == "" || slices.Contains(stock.EntryTypes, typ)),
		kind == "out" && (typ == "" || slices.Contains(stock.ExitTypes, typ)),
		(kind == "reversal" || kind == "adjust") && typ == "":
		f.Kind, f.Type = kind, typ
	default:
		fm.Type = ""
	}
	return f, fm
}

func validDate(text string) string {
	if _, err := time.Parse("2006-01-02", text); err != nil {
		return ""
	}
	return text
}

func (s *server) historyPage(w http.ResponseWriter, r *http.Request, user auth.User) {
	filter, fm := s.historyFilter(r)
	items, err := s.inventory.History(r.Context(), filter)
	if err != nil {
		s.serverError(w, err)
		return
	}
	medicines, err := s.inventory.Medicines(r.Context(), stock.MedicineFilter{IncludeArchived: true})
	if err != nil {
		s.serverError(w, err)
		return
	}
	pages := max(1, (len(items)+historyPageSize-1)/historyPageSize)
	page, _ := strconv.Atoi(r.URL.Query().Get("pagina"))
	page = min(max(page, 1), pages)
	shown := items[(page-1)*historyPageSize : min(page*historyPageSize, len(items))]

	view := historyView{Form: fm, Total: len(items), Medicines: medicines,
		EntryTypes: stock.EntryTypes, ExitTypes: stock.ExitTypes, CSVURL: "/historico.csv?" + filterQuery(fm).Encode()}
	for _, it := range shown {
		view.Rows = append(view.Rows, s.historyRow(it))
	}
	if pages > 1 {
		link := func(label string, n int, disabled bool) pagerLink {
			q := filterQuery(fm)
			q.Set("pagina", strconv.Itoa(n))
			return pagerLink{Label: label, URL: "/historico?" + q.Encode(), Current: n == page, Disabled: disabled}
		}
		view.Pager = append(view.Pager, link("Anterior", page-1, page == 1))
		for n := 1; n <= pages; n++ {
			view.Pager = append(view.Pager, link(strconv.Itoa(n), n, false))
		}
		view.Pager = append(view.Pager, link("Próxima", page+1, page == pages))
	}
	s.render(w, r, http.StatusOK, "history.html", pageData{Title: "Histórico", Active: "/historico", User: user, Page: view})
}

// filterQuery puts the filters back into a query string, so pages and the CSV keep them.
func filterQuery(fm historyForm) url.Values {
	return url.Values{"de": {fm.From}, "ate": {fm.To}, "tipo": {fm.Type},
		"medicamento": {fm.Medicine}, "lote": {fm.Lot}, "paciente": {fm.PatientName}}
}

func (s *server) historyRow(it stock.HistoryItem) historyRow {
	row := historyRow{HistoryItem: it, Label: kindLabels[it.Kind].label, Icon: kindLabels[it.Kind].icon,
		Subtitle: it.Type, UserFirst: it.UserName}
	if first, _, found := strings.Cut(it.UserName, " "); found {
		row.UserFirst = first
	}
	recorded := it.RecordedAt.In(s.local)
	row.When = formatDate(it.OccurredOn)
	if recorded.Format("2006-01-02") == it.OccurredOn {
		row.When += " " + recorded.Format("15:04")
	} else {
		row.Recorded = "registrado " + recorded.Format("02/01/2006 15:04")
	}
	switch it.Kind {
	case "adjust":
		row.First = it.Subject
		row.More = changesText(it.Changes)
		return row
	case "reversal":
		original := "saída"
		if it.ReversesKind == "in" {
			original = "entrada"
		}
		row.Subtitle = fmt.Sprintf("da %s de %s", original, formatDate(it.ReversesOn))
	}
	if len(it.Lines) > 0 {
		row.First = lineSummary(it.Kind, it.Lines[0])
	}
	switch rest := len(it.Lines) - 1; {
	case rest == 1:
		row.More = lineSummary(it.Kind, it.Lines[1])
	case rest > 1:
		row.More = fmt.Sprintf("e mais %d medicamentos", rest)
	}
	row.NoteShort = truncate(it.Note, 60)
	return row
}

// lineSummary reads "Dipirona 500 mg comprimido · 30". An estorno shows the direction: +30 or −30.
func lineSummary(kind string, l stock.HistoryLine) string {
	n := l.Quantity
	sign := ""
	if kind == "reversal" {
		sign = "+"
		if n < 0 {
			sign = "−"
		}
	}
	if n < 0 {
		n = -n
	}
	return fmt.Sprintf("%s · %s%d", l.Medicine, sign, n)
}

func changesText(changes []stock.Change) string {
	parts := make([]string, 0, len(changes))
	for _, c := range changes {
		parts = append(parts, fmt.Sprintf("%s %s → %s", strings.ToLower(c.Field), c.Old, c.New))
	}
	return strings.Join(parts, "; ")
}

func truncate(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n-1]) + "…"
}

// formatDate turns "2026-10-07" into "07/10/2026".
func formatDate(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return t.Format("02/01/2006")
}

// historyCSV exports the filtered history, one row per lot moved. First names are left out on
// purpose: a CSV leaves the system unencrypted and is never deleted (docs/PLAN.md, open item 2).
func (s *server) historyCSV(w http.ResponseWriter, r *http.Request, _ auth.User) {
	filter, _ := s.historyFilter(r)
	items, err := s.inventory.History(r.Context(), filter)
	if err != nil {
		s.serverError(w, err)
		return
	}
	rows := [][]string{{"Data do movimento", "Registrado em", "Movimento", "Tipo", "Medicamento", "Lote", "Fabricante",
		"Validade", "Quantidade", "Unidade", "Observação", "Usuário", "Estornada"}}
	for _, it := range items {
		row := s.historyRow(it)
		base := []string{formatDate(it.OccurredOn), it.RecordedAt.In(s.local).Format("02/01/2006 15:04"), row.Label, row.Subtitle}
		if it.Kind == "adjust" {
			rows = append(rows, append(base, it.Subject, "", "", "", "", "", changesText(it.Changes), it.UserName, ""))
			continue
		}
		reversed := ""
		if it.ReversedByID != 0 {
			reversed = "sim"
		}
		for _, l := range it.Lines {
			rows = append(rows, append(slices.Clone(base), l.Medicine, l.Lot, l.Manufacturer, l.Expiry,
				strconv.FormatInt(l.Quantity, 10), l.Unit, it.Note, it.UserName, reversed))
		}
	}
	filename := "historico-" + s.now().In(stock.TimeZone).Format("2006-01-02") + ".csv"
	if err := writeCSV(w, filename, rows); err != nil {
		s.serverError(w, err)
	}
}

type movementView struct {
	stock.MovementDetail
	Row         historyRow
	LogRows     []logRow
	EditingNote bool
	NoteValue   string // what goes in the note textarea
	ReverseBody string // the confirmation text for Estornar
}

// targetMovement loads the movement named in the URL, answering 404 when there is none.
func (s *server) targetMovement(w http.ResponseWriter, r *http.Request) (stock.MovementDetail, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return stock.MovementDetail{}, false
	}
	d, err := s.inventory.Movement(r.Context(), id)
	if errors.Is(err, stock.ErrMovementNotFound) {
		http.NotFound(w, r)
		return stock.MovementDetail{}, false
	}
	if err != nil {
		s.serverError(w, err)
		return stock.MovementDetail{}, false
	}
	return d, true
}

func (s *server) movementPage(w http.ResponseWriter, r *http.Request, user auth.User) {
	d, ok := s.targetMovement(w, r)
	if !ok {
		return
	}
	editing := user.IsAdmin() && r.URL.Query().Get("editar") == "observacao"
	s.renderMovement(w, r, user, http.StatusOK, d, editing, d.Note, nil)
}

func (s *server) renderMovement(w http.ResponseWriter, r *http.Request, user auth.User, status int,
	d stock.MovementDetail, editing bool, note string, errs form.Errors) {
	view := movementView{MovementDetail: d, Row: s.historyRow(d.HistoryItem), LogRows: movementLog(d), EditingNote: editing, NoteValue: note}
	if d.Reversible() {
		what, verb := "saída", "Devolve ao estoque"
		if d.Kind == "in" {
			what, verb = "entrada", "Retira do estoque"
		}
		var parts []string
		for _, l := range d.Lines {
			n := l.Quantity
			if n < 0 {
				n = -n
			}
			parts = append(parts, fmt.Sprintf("%d %s do lote %s (%s)", n, plural(l.Unit, n), l.Lot, l.Medicine))
		}
		view.ReverseBody = fmt.Sprintf("%s: %s. A %s original continua no histórico, marcada como estornada. "+
			"Um estorno não pode ser desfeito: se precisar, registre uma nova %s.", verb, strings.Join(parts, "; "), what, what)
	}
	s.render(w, r, status, "movement.html", pageData{
		Title: view.Row.Label + " · " + view.Row.Subtitle, Active: "/historico", User: user, Errors: errs, Page: view,
	})
}

func (s *server) updateNote(w http.ResponseWriter, r *http.Request, user auth.User) {
	d, ok := s.targetMovement(w, r)
	if !ok {
		return
	}
	note := r.PostForm.Get("note")
	changed, err := s.inventory.UpdateNote(r.Context(), d.MovementID, note, user.ID, s.now())
	var errs form.Errors
	if errors.As(err, &errs) {
		s.renderMovement(w, r, user, http.StatusUnprocessableEntity, d, true, note, errs)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	if changed {
		setFlash(w, r, "ok", "Observação atualizada. O histórico guarda o texto antigo e o novo.")
	}
	http.Redirect(w, r, fmt.Sprintf("/movimentos/%d", d.MovementID), http.StatusSeeOther)
}

func (s *server) reverseMovement(w http.ResponseWriter, r *http.Request, user auth.User) {
	d, ok := s.targetMovement(w, r)
	if !ok {
		return
	}
	_, err := s.inventory.Reverse(r.Context(), d.MovementID, user.ID, s.now())
	var blocked stock.ReverseBlockedError
	switch {
	case errors.As(err, &blocked):
		setFlash(w, r, "error", "Não dá para estornar: parte desta entrada já saiu do estoque.")
	case errors.Is(err, stock.ErrAlreadyReversed):
		setFlash(w, r, "error", "Esta movimentação já foi estornada.")
	case errors.Is(err, stock.ErrNotReversible):
		setFlash(w, r, "error", "Um estorno não pode ser estornado. Registre uma nova movimentação.")
	case err != nil:
		s.serverError(w, err)
		return
	case d.Kind == "in":
		setFlash(w, r, "ok", "Entrada estornada. O estoque foi atualizado.")
	default:
		setFlash(w, r, "ok", "Saída estornada. O estoque foi atualizado.")
	}
	http.Redirect(w, r, fmt.Sprintf("/movimentos/%d", d.MovementID), http.StatusSeeOther)
}
