package mcpserver

import (
	"encoding/json"
	"testing"
)

// A proposal stored before the text fields moved into edit.text must still
// carry its notes, description and directions.
func TestRecipeEditReadsAProposalOfTheEarlierShape(t *testing.T) {
	stored := `{"recipe":{"uid":"U","name":"Soup","edit":{"rating":3,"notes":"N","description":"","directions":"D"},
		"append_note":"","proposed":[{"field":"notes","from":"","to":"N"}]}}`
	var p payload
	if err := json.Unmarshal([]byte(stored), &p); err != nil {
		t.Fatal(err)
	}
	e := p.Recipe
	if e.UID != "U" || e.Name != "Soup" || e.Edit.Rating == nil || *e.Edit.Rating != 3 || len(e.Proposed) != 1 {
		t.Fatalf("decoded %+v", e)
	}
	if t2 := e.Edit.Text; len(t2) != 3 || t2["notes"] != "N" || t2["description"] != "" || t2["directions"] != "D" {
		t.Fatalf("text = %v", t2)
	}

	// The current shape round-trips unchanged.
	raw, _ := json.Marshal(p)
	var again payload
	if err := json.Unmarshal(raw, &again); err != nil {
		t.Fatal(err)
	}
	if t2 := again.Recipe.Edit.Text; len(t2) != 3 || t2["directions"] != "D" {
		t.Fatalf("round trip text = %v", t2)
	}
}
