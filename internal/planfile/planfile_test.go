package planfile

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/subpathdev/morgentraining/internal/training"
)

func TestParseSeedFiles(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "seed", "plan-morgen-5.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if f.Kind != KindPlan || f.Plan.Plan.Key != "morgen-5" || len(f.Plan.Plan.Blocks) != 5 {
		t.Fatalf("plan: %+v", f.Plan.Plan)
	}
	kinds := map[string]training.StepKind{}
	for _, e := range f.Plan.Exercises {
		kinds[e.Key] = e.Kind
	}
	if kinds["warmup"] != training.KindExercise || kinds["short-break"] != training.KindRest {
		t.Fatalf("kinds (default exercise): %v", kinds)
	}
}

func TestPeopleDefaults(t *testing.T) {
	f, err := Parse([]byte("people:\n  - {key: mariana, name: Mariana}\n"))
	if err != nil {
		t.Fatal(err)
	}
	want := training.PersonImport{Person: training.Person{Key: "mariana", Name: "Mariana", Language: training.DefaultLanguage}}
	if f.Kind != KindPeople || len(f.People) != 1 || f.People[0] != want {
		t.Fatalf("people: %+v", f.People)
	}
}

func TestParseRejectsUnknownFieldsAndFiles(t *testing.T) {
	for name, doc := range map[string]string{
		"typo in a step":   "plan: p\nblocks:\n  - steps: [{exercise: x, secnds: 5}]\n",
		"typo in a person": "people:\n  - {key: jan, nmae: Jan}\n",
		"neither":          "exercises: []\n",
		"not YAML":         "plan: [",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc)); err == nil {
				t.Fatal("want an error")
			} else if name == "typo in a step" && !strings.Contains(err.Error(), "secnds") {
				t.Fatalf("error should name the field: %v", err)
			}
		})
	}
}
