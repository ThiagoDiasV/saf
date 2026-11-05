package web

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
)

// writeCSV sends rows as a download that Excel in Portuguese opens directly: ";" between
// columns, CRLF line ends, UTF-8 with a byte order mark.
func writeCSV(w http.ResponseWriter, filename string, rows [][]string) error {
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	if _, err := w.Write([]byte("\xef\xbb\xbf")); err != nil {
		return err
	}
	out := csv.NewWriter(w)
	out.Comma = ';'
	out.UseCRLF = true
	for _, row := range rows {
		safe := make([]string, len(row))
		for i, cell := range row {
			safe[i] = csvCell(cell)
		}
		if err := out.Write(safe); err != nil {
			return err
		}
	}
	out.Flush()
	return out.Error()
}

// csvCell keeps a typed value like "=HYPERLINK(...)" from running as a formula when the file is
// opened in a spreadsheet. Numbers, negative ones included, stay numbers.
func csvCell(s string) string {
	if s == "" || !strings.ContainsRune("=+-@\t\r", rune(s[0])) {
		return s
	}
	if _, err := strconv.ParseInt(s, 10, 64); err == nil {
		return s
	}
	return "'" + s
}
