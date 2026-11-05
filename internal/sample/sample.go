// Package sample fills an empty database with fictitious medicines, lots and movements for the
// local trial (`saf` with no arguments). The real deployment (`saf serve`) never calls it.
package sample

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"time"

	"saf/internal/auth"
	"saf/internal/stock"
)

// Password is the trial logins' password, the same the prototype used. The trial only listens on
// the computer it runs on.
const Password = "farmacia2026"

// Logins of the trial users, shown in the trial window.
const (
	AdminLogin  = "paulo.mendes"
	ViewerLogin = "thiago.dias"
)

// Fill creates the trial users and a month of stock history ending at now: an expired lot, lots
// expiring soon, a medicine never received, an archived one, a corrected lot, a loss, a donation,
// dispensações (one with a photo) and an estorno. Expiry dates are relative to now, so the flags
// mean the same whenever the trial starts.
func Fill(ctx context.Context, accounts *auth.Accounts, inventory *stock.Inventory, now time.Time) error {
	f := filler{ctx: ctx, inv: inventory, now: now.In(stock.TimeZone)}
	adminID, err := accounts.Create(ctx, auth.NewUser{Name: "Paulo Mendes", Login: AdminLogin, Password: Password, Role: auth.RoleAdmin}, f.day(-31))
	if err != nil {
		return fmt.Errorf("create trial admin: %w", err)
	}
	if _, err := accounts.Create(ctx, auth.NewUser{Name: "Thiago Dias", Login: ViewerLogin, Password: Password, Role: auth.RoleViewer}, f.day(-31)); err != nil {
		return fmt.Errorf("create trial viewer: %w", err)
	}
	f.user = adminID

	for _, m := range []stock.MedicineInput{
		{Ingredient: "Dipirona", Strength: "500 mg", DosageForm: "Comprimido", Unit: "comprimido"},
		{Ingredient: "Amoxicilina", Strength: "500 mg", DosageForm: "Cápsula", Unit: "cápsula"},
		{Ingredient: "Paracetamol", Strength: "750 mg", DosageForm: "Comprimido", Unit: "comprimido"},
		{Ingredient: "Losartana potássica", Strength: "50 mg", DosageForm: "Comprimido", Unit: "comprimido"},
		{Ingredient: "Ibuprofeno", Strength: "50 mg/mL", DosageForm: "Suspensão oral", Unit: "frasco"},
		{Ingredient: "Captopril", Strength: "25 mg", DosageForm: "Comprimido", Unit: "comprimido"},
		{Ingredient: "Cetoconazol", Strength: "20 mg/g", DosageForm: "Creme", Unit: "bisnaga"},
	} {
		if _, err := inventory.CreateMedicine(ctx, m, f.day(-31)); err != nil {
			return fmt.Errorf("create %s: %w", m.Ingredient, err)
		}
	}

	f.entry(-30, "Saldo inicial", "Saldo inicial da planilha da farmácia.",
		line("Dipirona 500 mg comprimido", "X999", "EMS", f.month(-1), 10),
		line("Dipirona 500 mg comprimido", "B777", "Medley", f.month(2), 20),
		line("Dipirona 500 mg comprimido", "A123", "EMS", f.month(7), 240),
		line("Amoxicilina 500 mg cápsula", "K12", "Teuto", f.month(2), 60),
		line("Amoxicilina 500 mg cápsula", "K44", "Teuto", f.month(10), 84),
		line("Paracetamol 750 mg comprimido", "P750", "Medley", f.month(8), 20),
		line("Losartana potássica 50 mg comprimido", "LS9O", "Neo Química", f.month(15), 300),
		line("Ibuprofeno 50 mg/mL suspensão oral", "L2201", "Prati-Donaduzzi", f.month(19), 13),
		line("Captopril 25 mg comprimido", "C25", "EMS", f.month(-1), 20),
	)
	f.exit(-25, stock.Discard, "", "Lote vencido recolhido para descarte.", "Captopril 25 mg comprimido", "C25", 20)
	captopril, err := f.medicine("Captopril 25 mg comprimido")
	if err != nil {
		return err
	}
	if err := inventory.SetMedicineArchived(ctx, captopril.ID, true, f.user, f.day(-25)); err != nil {
		return fmt.Errorf("archive Captopril: %w", err)
	}
	if err := f.fixLot(-5, "Losartana potássica 50 mg comprimido", "LS9O", "LS90"); err != nil {
		return err
	}
	f.exit(-3, stock.Dispensing, "Pedro", "", "Paracetamol 750 mg comprimido", "P750", 20)
	f.exit(-2, "Perda", "", "Frasco quebrado na prateleira.", "Ibuprofeno 50 mg/mL suspensão oral", "L2201", 1)
	carla := f.exit(-1, stock.Dispensing, "Carla", "", "Dipirona 500 mg comprimido", "A123", 30)
	if f.err == nil {
		if _, err := inventory.Reverse(ctx, carla, f.user, f.day(-1).Add(10*time.Minute)); err != nil {
			return fmt.Errorf("reverse Carla's dispensação: %w", err)
		}
	}
	f.entry(-1, "Doação", "Doação da Clínica Escola de Saúde.",
		line("Amoxicilina 500 mg cápsula", "K44", "Teuto", f.month(10), 40))
	joao := f.exit(0, stock.Dispensing, "João", "Receita da UBS Centro.", "Dipirona 500 mg comprimido", "A123", 12)
	if f.err != nil {
		return f.err
	}
	photo, err := prescriptionPhoto()
	if err != nil {
		return err
	}
	return inventory.AddPhotos(ctx, joao, [][]byte{photo}, f.user, f.day(0))
}

// filler keeps the first error, so Fill reads as a list of movements.
type filler struct {
	ctx  context.Context
	inv  *stock.Inventory
	now  time.Time
	user int64
	err  error
}

// day is 10:00 on the day offset days from now (a movement saved during opening hours).
func (f *filler) day(offset int) time.Time {
	d := f.now.AddDate(0, 0, offset)
	return time.Date(d.Year(), d.Month(), d.Day(), 10, 0, 0, 0, stock.TimeZone)
}

// month is the expiry "MM/AAAA" offset months from now.
func (f *filler) month(offset int) string {
	return f.now.AddDate(0, offset, 1-f.now.Day()).Format("01/2006")
}

func line(medicine, lot, manufacturer, expiry string, quantity int) stock.EntryLine {
	return stock.EntryLine{Medicine: medicine, Lot: lot, Manufacturer: manufacturer, Expiry: expiry, Quantity: fmt.Sprint(quantity)}
}

func (f *filler) entry(offset int, typ, note string, lines ...stock.EntryLine) {
	if f.err != nil {
		return
	}
	at := f.day(offset)
	if _, err := f.inv.RecordEntry(f.ctx, stock.Entry{Type: typ, Date: at.Format("2006-01-02"), Note: note, Lines: lines}, f.user, at); err != nil {
		f.err = fmt.Errorf("sample %s: %w", typ, err)
	}
}

// exit takes quantity from one lot and returns the movement id.
func (f *filler) exit(offset int, typ, name, note, medicine, lot string, quantity int64) int64 {
	if f.err != nil {
		return 0
	}
	m, err := f.medicine(medicine)
	if err != nil {
		f.err = err
		return 0
	}
	batches, err := f.inv.AvailableBatches(f.ctx, m.ID, f.day(offset))
	if err != nil {
		f.err = err
		return 0
	}
	takes := map[int64]string{}
	for _, b := range batches {
		if b.Lot == lot {
			takes[b.ID] = fmt.Sprint(quantity)
		}
	}
	at := f.day(offset)
	if typ == "Perda" { // recorded a day late, to show a backdated movement
		at = f.day(offset + 1)
	}
	id, err := f.inv.RecordExit(f.ctx, stock.Exit{Type: typ, Date: f.day(offset).Format("2006-01-02"), Note: note, PatientName: name,
		Lines: []stock.ExitLine{{Medicine: medicine, Quantity: fmt.Sprint(quantity), Takes: takes}}}, f.user, at)
	if err != nil {
		f.err = fmt.Errorf("sample %s of %s: %w", typ, medicine, err)
	}
	return id
}

func (f *filler) medicine(label string) (stock.Medicine, error) {
	m, found, err := f.inv.ActiveMedicineByLabel(f.ctx, label)
	if err == nil && !found {
		err = fmt.Errorf("sample medicine %q not found", label)
	}
	return m, err
}

// fixLot corrects a lot's typo, which shows up as an Ajuste in Histórico.
func (f *filler) fixLot(offset int, medicine, from, to string) error {
	if f.err != nil {
		return f.err
	}
	m, err := f.medicine(medicine)
	if err != nil {
		return err
	}
	batches, err := f.inv.AvailableBatches(f.ctx, m.ID, f.day(offset))
	if err != nil {
		return err
	}
	for _, b := range batches {
		if b.Lot == from {
			_, err := f.inv.UpdateBatch(f.ctx, b.ID, stock.BatchInput{Lot: to, Manufacturer: b.Manufacturer, Expiry: b.Expiry()}, f.user, f.day(offset))
			return err
		}
	}
	return fmt.Errorf("sample lot %s of %s not found", from, medicine)
}

// prescriptionPhoto draws a sheet with lines of "text", standing in for a photographed prescription.
func prescriptionPhoto() ([]byte, error) {
	img := image.NewRGBA(image.Rect(0, 0, 900, 1200))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{238, 236, 230, 255}}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(60, 60, 840, 1140), &image.Uniform{color.White}, image.Point{}, draw.Src)
	ink := &image.Uniform{color.RGBA{70, 80, 110, 255}}
	draw.Draw(img, image.Rect(120, 130, 520, 160), ink, image.Point{}, draw.Src)
	for i, width := range []int{620, 540, 660, 300, 580, 480, 640, 360} {
		y := 260 + i*80
		draw.Draw(img, image.Rect(120, y, 120+width, y+18), ink, image.Point{}, draw.Src)
	}
	draw.Draw(img, image.Rect(480, 1000, 760, 1006), ink, image.Point{}, draw.Src)
	var out bytes.Buffer
	if err := jpeg.Encode(&out, img, &jpeg.Options{Quality: 80}); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
