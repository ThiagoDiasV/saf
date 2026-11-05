package stock

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFold(t *testing.T) {
	for in, want := range map[string]string{
		"Dipirona Sódica 500 mg":    "dipironasodica500mg",
		"DIPIRONA  SODICA 500MG":    "dipironasodica500mg",
		"Ácido Acetilsalicílico":    "acidoacetilsalicilico",
		"Solução injetável":         "solucaoinjetavel",
		"Sacarato de óxido férrico": "sacaratodeoxidoferrico",
		"Pomada\tnasal\n":           "pomadanasal",
		"Clorexidina 0,12%":         "clorexidina0,12%",
		"López":                    "lopez", // accent sent as a separate combining character
	} {
		assert.Equal(t, want, fold(in), in)
	}
}

func TestClean(t *testing.T) {
	assert.Equal(t, "Dipirona sódica 500 mg", clean("  Dipirona   sódica\t500 mg "))
}
