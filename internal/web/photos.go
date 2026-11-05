package web

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"time"

	"saf/internal/auth"
	"saf/internal/stock"
)

// photoUploads reads the form's photos (the browser already shrank them) and normalizes each.
// A problem the user can fix comes back as a message; anything else as an error.
func photoUploads(r *http.Request) ([][]byte, string, error) {
	if r.MultipartForm == nil {
		return nil, "", nil
	}
	files := r.MultipartForm.File["photos"]
	if len(files) > stock.MaxPhotosPerUpload {
		return nil, fmt.Sprintf("No máximo %d fotos por vez.", stock.MaxPhotosPerUpload), nil
	}
	var photos [][]byte
	for _, fh := range files {
		f, err := fh.Open()
		if err != nil {
			return nil, "", err
		}
		raw, err := io.ReadAll(f)
		f.Close()
		if err != nil {
			return nil, "", err
		}
		photo, err := stock.NormalizePhoto(raw)
		switch {
		case errors.Is(err, stock.ErrNotAPhoto):
			return nil, "Um dos arquivos não é uma foto. Use JPEG ou PNG.", nil
		case errors.Is(err, stock.ErrPhotoTooLarge):
			return nil, "Uma das fotos é grande demais. Anexe pelo SAF, que reduz a foto antes de enviar.", nil
		case err != nil:
			return nil, "", err
		}
		photos = append(photos, photo)
	}
	return photos, "", nil
}

const photosAgain = "Escolha as fotos de novo: elas não ficam guardadas quando há erro."

// attachPhotos saves the photos of a movement just recorded. The movement stands even if this
// fails; the flash tells the user to add them from its detail page.
func (s *server) attachPhotos(w http.ResponseWriter, r *http.Request, movementID int64, photos [][]byte,
	userID int64, now time.Time, saved string) bool {
	if len(photos) == 0 {
		return true
	}
	if err := s.inventory.AddPhotos(r.Context(), movementID, photos, userID, now); err != nil {
		slog.Error("save photos", "movement", movementID, "err", err)
		setFlash(w, r, "error", saved+" registrada, mas as fotos não foram salvas. Adicione pelo detalhe, no Histórico.")
		return false
	}
	return true
}

// photoPicker is what the "photo-picker" template draws.
type photoPicker struct {
	Hint  string
	Error string
}

func (s *server) photo(w http.ResponseWriter, r *http.Request, _ auth.User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	jpeg, err := s.inventory.PhotoJPEG(r.Context(), id)
	if errors.Is(err, stock.ErrPhotoNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Content-Disposition", "inline")
	w.Write(jpeg)
}

func (s *server) addPhotos(w http.ResponseWriter, r *http.Request, user auth.User) {
	d, ok := s.targetMovement(w, r)
	if !ok {
		return
	}
	back := fmt.Sprintf("/movimentos/%d#fotos", d.MovementID)
	photos, problem, err := photoUploads(r)
	switch {
	case err != nil:
		s.serverError(w, err)
		return
	case problem != "":
		setFlash(w, r, "error", problem)
	case len(photos) == 0:
		setFlash(w, r, "error", "Escolha pelo menos uma foto.")
	default:
		if err := s.inventory.AddPhotos(r.Context(), d.MovementID, photos, user.ID, s.now()); err != nil {
			s.serverError(w, err)
			return
		}
		setFlash(w, r, "ok", photosAdded(len(photos)))
	}
	http.Redirect(w, r, back, http.StatusSeeOther)
}

func photosAdded(n int) string {
	if n == 1 {
		return "Foto adicionada."
	}
	return fmt.Sprintf("%d fotos adicionadas.", n)
}

func (s *server) removePhoto(w http.ResponseWriter, r *http.Request, user auth.User) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	movementID, err := s.inventory.RemovePhoto(r.Context(), id, user.ID, s.now())
	if errors.Is(err, stock.ErrPhotoNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	setFlash(w, r, "ok", "Foto removida.")
	http.Redirect(w, r, fmt.Sprintf("/movimentos/%d#fotos", movementID), http.StatusSeeOther)
}

// logRow is one line of a movement's "Registro de alterações": an observação edit or a photo
// added later, removed, or deleted by the 6-month rule.
type logRow struct {
	At     time.Time
	User   string        // "" for the automatic deletion
	Change *stock.Change // set for observação edits
	Event  string        // for photos
}

func movementLog(d stock.MovementDetail) []logRow {
	var rows []logRow
	for i := range d.Log {
		rows = append(rows, logRow{At: d.Log[i].ChangedAt, User: d.Log[i].UserName, Change: &d.Log[i]})
	}
	for _, p := range d.Photos {
		if p.AddedAt.After(d.RecordedAt) {
			rows = append(rows, logRow{At: p.AddedAt, User: p.AddedBy, Event: "foto adicionada"})
		}
		switch {
		case p.Removed() && p.RemovedBy == "":
			rows = append(rows, logRow{At: p.RemovedAt, Event: "foto apagada (prazo de 6 meses)"})
		case p.Removed():
			rows = append(rows, logRow{At: p.RemovedAt, User: p.RemovedBy, Event: "foto removida"})
		}
	}
	slices.SortStableFunc(rows, func(a, b logRow) int { return a.At.Compare(b.At) })
	return rows
}
