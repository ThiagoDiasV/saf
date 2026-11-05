package web

import (
	"bytes"
	"context"
	"image"
	"image/jpeg"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"saf/internal/auth"
)

func smallJPEG(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	require.NoError(t, jpeg.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 16, 12)), nil))
	return buf.Bytes()
}

// postMultipart sends fields and files (all named "photos") the way a form with photos does.
func (app *testApp) postMultipart(t *testing.T, path string, fields map[string]string, files [][]byte, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	for k, v := range fields {
		require.NoError(t, mw.WriteField(k, v))
	}
	for i, data := range files {
		part, err := mw.CreateFormFile("photos", "foto"+strconv.Itoa(i)+".jpg")
		require.NoError(t, err)
		_, err = part.Write(data)
		require.NoError(t, err)
	}
	require.NoError(t, mw.Close())
	r := httptest.NewRequest(http.MethodPost, path, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	return app.serve(r, cookies...)
}

func (app *testApp) photoIDs(t *testing.T, movementID int64) []int64 {
	t.Helper()
	photos, err := app.inventory.Photos(context.Background(), movementID)
	require.NoError(t, err)
	var ids []int64
	for _, p := range photos {
		if !p.Removed() {
			ids = append(ids, p.ID)
		}
	}
	return ids
}

func photoPath(id int64, action string) string { return "/fotos/" + strconv.FormatInt(id, 10) + action }

var entryWithOneLine = map[string]string{
	"type": "Doação", "date": "2026-10-07", "medicine": "Dipirona 500 mg comprimido", "lot": "A123",
	"manufacturer": "EMS", "expiry": "03/2027", "quantity": "10",
}

func TestEntryWithPhotos(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	app.addMedicine(t, dipironaInput())

	res := app.postMultipart(t, "/entrada", entryWithOneLine, [][]byte{smallJPEG(t), smallJPEG(t)}, admin)

	require.Equal(t, http.StatusSeeOther, res.Code, res.Body.String())
	var movementID int64
	require.NoError(t, app.conn.QueryRow(`SELECT id FROM movements`).Scan(&movementID))
	ids := app.photoIDs(t, movementID)
	require.Len(t, ids, 2)

	photo := app.get(photoPath(ids[0], ""), admin)
	require.Equal(t, http.StatusOK, photo.Code)
	assert.Equal(t, "image/jpeg", photo.Header().Get("Content-Type"))
	assert.Equal(t, "no-store", photo.Header().Get("Cache-Control"))
	assert.Equal(t, "same-origin", photo.Header().Get("Cross-Origin-Resource-Policy"))
	_, format, err := image.DecodeConfig(photo.Body)
	require.NoError(t, err)
	assert.Equal(t, "jpeg", format)

	assert.Equal(t, "/login", app.get(photoPath(ids[0], "")).Header().Get("Location"), "photos need a login")
	assert.Contains(t, app.get("/historico?tipo=in", admin).Body.String(), `<td data-label="Fotos" class="num">2</td>`)
}

func TestAPhotoProblemSavesNothing(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	app.addMedicine(t, dipironaInput())

	res := app.postMultipart(t, "/entrada", entryWithOneLine, [][]byte{[]byte("%PDF-1.7 não é foto")}, admin)

	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	assert.Contains(t, res.Body.String(), "Um dos arquivos não é uma foto. Use JPEG ou PNG.")
	assert.Contains(t, res.Body.String(), `value="A123"`, "the form comes back filled")
	var movements int
	require.NoError(t, app.conn.QueryRow(`SELECT COUNT(*) FROM movements`).Scan(&movements))
	assert.Zero(t, movements)

	many := make([][]byte, 11)
	for i := range many {
		many[i] = smallJPEG(t)
	}
	assert.Contains(t, app.postMultipart(t, "/entrada", entryWithOneLine, many, admin).Body.String(), "No máximo 10 fotos por vez.")
}

func TestFormErrorsAskForThePhotosAgain(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	app.addMedicine(t, dipironaInput())
	fields := map[string]string{"type": "Dispensação", "date": "2026-10-07", "lines.0.medicine": "Ibuprofeno", "lines.0.quantity": "1"}

	res := app.postMultipart(t, "/saida", fields, [][]byte{smallJPEG(t)}, admin)

	assert.Equal(t, http.StatusUnprocessableEntity, res.Code)
	assert.Contains(t, res.Body.String(), "Escolha as fotos de novo: elas não ficam guardadas quando há erro.")
}

func TestUploadTooLargeIsRefused(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)

	res := app.postMultipart(t, "/entrada", entryWithOneLine, [][]byte{bytes.Repeat([]byte("x"), maxUploadBytes)}, admin)

	assert.Equal(t, http.StatusRequestEntityTooLarge, res.Code)
}

func TestAddAndRemovePhotosOnTheDetailPage(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	_, exitID := app.seedHistory(t)

	res := app.postMultipart(t, movementPath(exitID, "/fotos"), nil, nil, admin)
	assert.Contains(t, app.get(movementPath(exitID, ""), admin, cookieNamed(res, flashCookie)).Body.String(), "Escolha pelo menos uma foto.")

	app.now = app.now.Add(2 * time.Hour)
	res = app.postMultipart(t, movementPath(exitID, "/fotos"), nil, [][]byte{smallJPEG(t)}, admin)
	require.Equal(t, http.StatusSeeOther, res.Code)
	assert.Equal(t, movementPath(exitID, "#fotos"), res.Header().Get("Location"))
	body := app.get(movementPath(exitID, ""), admin, cookieNamed(res, flashCookie)).Body.String()
	assert.Contains(t, body, "Foto adicionada.")
	ids := app.photoIDs(t, exitID)
	require.Len(t, ids, 1)
	assert.Contains(t, body, `class="thumb-img blurred" data-photo-view="`+photoPath(ids[0], "")+`"`, "blurred until clicked")
	assert.Contains(t, body, "Paulo Mendes: foto adicionada")

	res = app.post(photoPath(ids[0], "/remover"), nil, admin)
	require.Equal(t, http.StatusSeeOther, res.Code)
	body = app.get(movementPath(exitID, ""), admin, cookieNamed(res, flashCookie)).Body.String()
	assert.Contains(t, body, "Foto removida.")
	assert.Contains(t, body, "Paulo Mendes: foto removida")
	assert.NotContains(t, body, "data-photo-view")
	assert.Equal(t, http.StatusNotFound, app.get(photoPath(ids[0], ""), admin).Code)
	assert.Equal(t, http.StatusNotFound, app.post(photoPath(ids[0], "/remover"), nil, admin).Code)
}

func TestViewerSeesPhotosButCannotChangeThem(t *testing.T) {
	app := newTestApp(t, "")
	app.addUser(t, "Thiago Dias", "thiago.dias", auth.RoleViewer)
	_, exitID := app.seedHistory(t)
	require.NoError(t, app.inventory.AddPhotos(context.Background(), exitID, [][]byte{smallJPEG(t)}, 1, app.now))
	viewer := app.login(t, "thiago.dias", testPassword)
	id := app.photoIDs(t, exitID)[0]

	body := app.get(movementPath(exitID, ""), viewer).Body.String()

	assert.Contains(t, body, "data-photo-view")
	assert.NotContains(t, body, "Adicionar foto")
	assert.NotContains(t, body, "/remover")
	assert.Equal(t, http.StatusOK, app.get(photoPath(id, ""), viewer).Code)
	assert.Equal(t, http.StatusForbidden, app.post(photoPath(id, "/remover"), nil, viewer).Code)
	assert.True(t, strings.Contains(app.get("/historico?tipo=out", viewer).Body.String(), `<td data-label="Fotos" class="num">1</td>`))
}

func TestPurgedPhotosAndNamesShowAsDeleted(t *testing.T) {
	app := newTestApp(t, "")
	admin := adminSession(t, app)
	_, exitID := app.seedHistory(t)
	require.NoError(t, app.inventory.AddPhotos(context.Background(), exitID, [][]byte{smallJPEG(t)}, 1, app.now.Add(time.Hour)))

	_, err := app.inventory.Purge(context.Background(), app.now.AddDate(0, 7, 0))
	require.NoError(t, err)

	body := app.get(movementPath(exitID, ""), admin).Body.String()
	assert.Contains(t, body, "foto apagada (prazo de 6 meses)")
	assert.NotContains(t, body, "data-photo-view")
	assert.NotContains(t, body, "Dispensado para", "the first name is gone too")
	list := app.get("/historico?tipo=out&de=&ate=", admin).Body.String()
	assert.Contains(t, list, `<td data-label="Dispensado para">—</td>`)
	assert.Contains(t, list, `<td data-label="Fotos" class="num">0</td>`)
}
