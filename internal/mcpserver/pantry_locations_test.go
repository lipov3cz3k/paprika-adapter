package mcpserver_test

import (
	"strings"
	"testing"
)

const (
	pantryLoc  = "cc5dffa2-a02b-409a-85e8-e240f2ffaee4" // the default location
	fridgeLoc  = "01A1CFCE-E10D-7000-B62E-3C33882FCC6F"
	freezerLoc = "01A1CFCE-F290-7000-9CFC-BE283C2FB5D8"
	garlicUID  = "27C8E0A4-5D36-4F1B-8E92-B4A7D3F6C015"
	riceUID    = "8A0F4C62-1B7E-4D95-A2C3-E9D6B5F7A840"
)

func storedPantryRow(h *harness, uid string) map[string]any {
	h.t.Helper()
	for _, row := range h.fake.Rows("pantry") {
		if row["uid"] == uid {
			return row
		}
	}
	h.t.Fatalf("Paprika holds no pantry row %s", uid)
	return nil
}

func TestListPantryShowsLocationsAndNotes(t *testing.T) {
	h := newHarness(t, false)
	r := h.ok("list_pantry", nil)
	var names []string
	for _, l := range r.Locations {
		names = append(names, l.Name)
	}
	if strings.Join(names, ",") != "Pantry,Fridge,Freezer" {
		t.Fatalf("locations = %v, want the app's order", names)
	}
	byName := map[string]item{}
	for _, it := range r.Items {
		byName[it.Ingredient] = it
	}
	if g := byName["Garlic"]; g.Location != "Fridge" || g.LocationUID != fridgeLoc || g.Notes != "half a bulb" {
		t.Fatalf("garlic = %+v", g)
	}
	if rice := byName["Rice"]; rice.Location != "Pantry" || rice.LocationUID != "" || rice.Notes != "" {
		t.Fatalf("rice = %+v, want the default location", rice)
	}
}

func TestListPantryFiltersByLocation(t *testing.T) {
	h := newHarness(t, false)
	r := h.ok("list_pantry", map[string]any{"location": " fridge "})
	if r.Total != 2 || r.Items[0].Ingredient != "Garlic" || r.Items[1].Ingredient != "Soy sauce" {
		t.Fatalf("fridge = %+v", r.Items)
	}
	if def := h.ok("list_pantry", map[string]any{"location": "Pantry"}); def.Total != 5 {
		t.Fatalf("default location holds %d items, want the 5 without a location", def.Total)
	}
	f := h.fails("list_pantry", map[string]any{"location": "Cellar"}, "invalid_input")
	if !strings.Contains(f.Error.Message, "Pantry, Fridge, Freezer") || !strings.Contains(f.Error.Message, "Paprika app") {
		t.Fatalf("error = %s", f.Error.Message)
	}
}

func TestPantryItemsAreAddedInALocationWithNotes(t *testing.T) {
	h := newHarness(t, true)
	p := h.ok("propose_pantry_items", map[string]any{"items": []any{
		map[string]any{"ingredient": "Milk", "quantity": "1 l", "location": "fridge", "notes": "lactose free"},
		map[string]any{"ingredient": "Peas", "location": "Freezer"},
		map[string]any{"ingredient": "Pasta"}, // no location: the default one, sent without a uid
	}}).Proposal
	if len(p.Pantry) != 3 || p.Pantry[0].Location != "Fridge" || p.Pantry[0].Notes != "lactose free" || p.Pantry[2].Location != "Pantry" {
		t.Fatalf("proposal = %+v", p.Pantry)
	}
	if !strings.Contains(p.Summary, "1 l Milk (Fridge) [note: lactose free]; Peas (Freezer); Pasta.") {
		t.Fatalf("summary = %s", p.Summary)
	}
	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if !c.Verified || c.Pantry[0].Location != "Fridge" || c.Pantry[1].Location != "Freezer" {
		t.Fatalf("commit = %+v", c.Pantry)
	}
	if row := storedPantryRow(h, p.Pantry[0].UID); row["location_uid"] != fridgeLoc || row["notes"] != "lactose free" {
		t.Fatalf("milk row = %v", row)
	}
	if row := storedPantryRow(h, p.Pantry[2].UID); row["location_uid"] != nil || row["notes"] != nil {
		t.Fatalf("pasta row = %v, want neither field sent", row)
	}
}

func TestPantryItemInAnUnknownLocationIsRefused(t *testing.T) {
	h := newHarness(t, true)
	f := h.fails("propose_pantry_items", map[string]any{"items": []any{
		map[string]any{"ingredient": "Ice cream", "location": "Garage freezer"},
	}}, "invalid_input")
	if !strings.Contains(f.Error.Message, "items[0].location") || !strings.Contains(f.Error.Message, "Fridge") {
		t.Fatalf("error = %s", f.Error.Message)
	}
	if h.posts("pantry") != 0 {
		t.Fatal("wrote to Paprika")
	}
}

func TestPantryEditMovesAnItemAndRemovesItsNote(t *testing.T) {
	h := newHarness(t, true)
	p := h.ok("propose_pantry_edit", map[string]any{"item_uid": garlicUID, "location": "Freezer", "notes": ""}).Proposal
	if p.PantryBefore[0].Location != "Fridge" || p.Pantry[0].Location != "Freezer" || p.Pantry[0].Notes != "" {
		t.Fatalf("before %+v after %+v", p.PantryBefore, p.Pantry)
	}
	if !strings.Contains(p.Summary, "remove the notes; move it to Freezer") {
		t.Fatalf("summary = %s", p.Summary)
	}
	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if !c.Verified || c.Pantry[0].Location != "Freezer" {
		t.Fatalf("commit = %+v", c.Pantry)
	}
	row := storedPantryRow(h, garlicUID)
	if row["location_uid"] != freezerLoc || row["notes"] != nil || row["ingredient"] != "Garlic" {
		t.Fatalf("garlic row = %v", row)
	}
}

func TestPantryEditToTheLocationAnItemIsInChangesNothing(t *testing.T) {
	h := newHarness(t, true)
	h.fails("propose_pantry_edit", map[string]any{"item_uid": garlicUID, "location": "Fridge"}, "duplicate")
	// An item without a location already is in the default one.
	h.fails("propose_pantry_edit", map[string]any{"item_uid": riceUID, "location": "pantry"}, "duplicate")
}

func TestPantryEditSetsANote(t *testing.T) {
	h := newHarness(t, true)
	p := h.ok("propose_pantry_edit", map[string]any{"item_uid": riceUID, "notes": " opened 2026-10-01 "}).Proposal
	h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if row := storedPantryRow(h, riceUID); row["notes"] != "opened 2026-10-01" || row["location_uid"] != nil {
		t.Fatalf("rice row = %v", row)
	}
}
