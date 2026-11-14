package mcpserver_test

import (
	"fmt"
	"strings"
	"testing"
)

const (
	milk        = "0A91A12D-74D3-4987-BBBE-C36D3FABF505" // Milk, Dairy, My Grocery List
	lemons      = "61DFC31A-BCBF-4912-81E3-EEF8B061DFE2" // 2 lemons, from a recipe
	eggs        = "4D7B2E90-A1C6-4F35-8B09-E6D3C5A7F182" // Eggs, already purchased
	butter      = "93C5F0A8-7E14-4D62-B3A9-1F8E6D2C0B47" // Butter
	paperTowels = "B2E6D4A1-3C97-4F08-A5D1-7C0B9E8F2A63" // on the Costco list
)

func groceryRow(h *harness, uid string) map[string]any {
	for _, r := range h.fake.Rows("groceries") {
		if r["uid"] == uid {
			return r
		}
	}
	return nil
}

func TestGroceryEditProposeCommitReadBack(t *testing.T) {
	h := newHarness(t, true)
	row := groceryRow(h, milk)
	row["field_from_a_newer_paprika"] = "keep me"
	h.fake.Put("groceries", row)

	p := h.ok("propose_grocery_edit", map[string]any{"item_uid": milk, "name": "oat milk", "quantity": "2 l",
		"instruction": "the barista one", "aisle": "baking goods", "list_uid": costcoList}).Proposal
	if p.Kind != "grocery_edit" || len(p.GroceryAfter) != 1 || len(p.GroceryBefore) != 1 {
		t.Fatalf("proposal = %+v", p)
	}
	if a := p.GroceryAfter[0]; a.Name != "2 l oat milk" || a.Ingredient != "oat milk" || a.Quantity != "2 l" || a.Instruction != "the barista one" ||
		a.Aisle != "Baking Goods" || a.ListUID != costcoList || a.Purchased {
		t.Fatalf("after = %+v", a)
	}
	want := `Change the item "Milk" on the Paprika grocery list "My Grocery List": change it to "2 l oat milk"; set the note to "the barista one"; ` +
		`put it in the aisle Baking Goods; move it to the list "Costco". This overwrites the item.`
	if p.Summary != want {
		t.Fatalf("summary = %s", p.Summary)
	}
	if h.posts("groceries") != 0 {
		t.Fatal("proposing wrote to Paprika")
	}

	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if c.Status != "committed" || !c.Verified || len(c.Items) != 1 || c.Items[0].Name != "2 l oat milk" || len(c.GroceryBefore) != 1 ||
		c.GroceryBefore[0].Name != "Milk" || h.posts("groceries") != 1 {
		t.Fatalf("commit = %+v", c)
	}
	row = groceryRow(h, milk)
	if row["name"] != "2 l oat milk" || row["ingredient"] != "oat milk" || row["quantity"] != "2 l" || row["list_uid"] != costcoList ||
		row["aisle"] != "Baking Goods" || row["aisle_uid"] != "AFADC523FB4CFA907914740E22016338CBD90DDFA42177B87F9E46F2B376EB28" || row["order_flag"] != float64(1) {
		t.Fatalf("row in Paprika = %v", row)
	}
	if row["field_from_a_newer_paprika"] != "keep me" || row["separate"] != false {
		t.Fatalf("a field the edit did not name was lost: %v", row)
	}
	if len(h.fake.Rows("groceries")) != 5 {
		t.Fatalf("the edit created or removed a row: %d items", len(h.fake.Rows("groceries")))
	}
	if r := h.ok("list_grocery_items", map[string]any{"list_uid": costcoList}); r.Total != 2 {
		t.Fatalf("Costco after the edit = %+v", r.Items)
	}
	if again := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}); again.Status != "already_committed" || h.posts("groceries") != 1 {
		t.Fatalf("repeat = %+v posts=%d", again, h.posts("groceries"))
	}
}

func TestGroceryEditKeepsWhatIsNotNamed(t *testing.T) {
	h := newHarness(t, true)
	// A new quantity keeps the item's own wording; a lone purchased flips only that.
	p := h.ok("propose_grocery_edit", map[string]any{"item_uid": lemons, "quantity": "3", "purchased": true}).Proposal
	if a := p.GroceryAfter[0]; a.Name != "3 lemons" || a.Ingredient != "lemons" || !a.Purchased || a.Recipe != "Lemon Garlic Roast Chicken" {
		t.Fatalf("after = %+v", a)
	}
	if !strings.Contains(p.Summary, `change it to "3 lemons"; tick it off as bought`) {
		t.Fatalf("summary = %s", p.Summary)
	}
	h.fake.DropNextWriteAck()
	if r := h.fails("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}, "upstream_unavailable"); !r.Error.Retryable {
		t.Fatalf("error = %+v", r.Error)
	}
	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	row := groceryRow(h, lemons)
	if !c.Verified || h.posts("groceries") != 1 || row["purchased"] != true || row["recipe_uid"] != roastUID || row["list_uid"] != defaultList {
		t.Fatalf("retry = %+v posts=%d row=%v", c, h.posts("groceries"), row)
	}
}

func TestGroceryEditNewQuantityReplacesTheOldOne(t *testing.T) {
	for _, c := range []struct{ name, ingredient, quantity, want string }{
		{"Milk", "milk", "", "3 l Milk"},
		{"2 l Milk", "milk", "2 l", "3 l Milk"},
		{"2 L milk", "milk", "2 l", "3 l milk"},
		{"2l milk", "milk", "2 l", "3 l milk"},
		{"2 lemons", "lemons", "", "3 l lemons"},      // amount typed into the line only
		{"2\u00a0lemons", "lemons", "", "3 l lemons"}, // a no-break space after the amount, as pasted
		{"500g flour, fine", "flour", "", "3 l flour, fine"},
		{"Milk (2 l)", "milk", "2 l", "3 l milk"}, // amount inside the line: the bare ingredient
		{"7up", "7up", "", "3 l 7up"},
		{"7 Up", "7 up", "", "3 l 7 Up"},                                // the number is part of the name
		{"00 flour", "00 flour", "", "3 l 00 flour"},                    // likewise
		{"Organic Whole Milk", "milk", "2 l", "3 l Organic Whole Milk"}, // the amount is not in the line at all
		{"Milk 12%", "milk", "2", "3 l Milk 12%"},                       // "2" only inside another number
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, true)
			row := groceryRow(h, milk)
			row["name"], row["ingredient"], row["quantity"] = c.name, c.ingredient, c.quantity
			h.fake.Put("groceries", row)
			p := h.ok("propose_grocery_edit", map[string]any{"item_uid": milk, "quantity": "3 l"}).Proposal
			if got := p.GroceryAfter[0].Name; got != c.want {
				t.Fatalf("line = %q, want %q", got, c.want)
			}
		})
	}
}

func TestGroceryEditKeepsARenameMadeMeanwhile(t *testing.T) {
	h := newHarness(t, true)
	p := h.ok("propose_grocery_edit", map[string]any{"item_uid": milk, "quantity": "2 l"}).Proposal
	note := h.ok("propose_grocery_edit", map[string]any{"item_uid": milk, "instruction": "skimmed"}).Proposal
	row := groceryRow(h, milk)
	row["name"], row["ingredient"] = "Oat milk", "oat milk" // renamed in the app
	h.fake.Put("groceries", row)
	h.fails("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}, "uid_conflict")
	if h.posts("groceries") != 0 || groceryRow(h, milk)["name"] != "Oat milk" {
		t.Fatal("the edit overwrote a rename made in the app")
	}
	// An edit that leaves the wording alone still applies on top of the rename.
	h.ok("commit_proposal", map[string]any{"proposal_id": note.ProposalID, "confirm": true})
	if row := groceryRow(h, milk); row["name"] != "Oat milk" || row["instruction"] != "skimmed" {
		t.Fatalf("row = %v", row)
	}
}

func TestGroceryEditSummaryNamesAnAmountTheLineDoesNotShow(t *testing.T) {
	h := newHarness(t, true)
	row := groceryRow(h, milk)
	row["name"], row["quantity"] = "2 l milk", "" // amount typed into the line only
	h.fake.Put("groceries", row)
	p := h.ok("propose_grocery_edit", map[string]any{"item_uid": milk, "quantity": "2 l"}).Proposal
	if !strings.Contains(p.Summary, `set the stored amount to "2 l"`) || strings.Contains(p.Summary, "ingredient") {
		t.Fatalf("summary = %s", p.Summary)
	}
	p = h.ok("propose_grocery_edit", map[string]any{"item_uid": milk, "quantity": "3 l"}).Proposal
	if !strings.Contains(p.Summary, `change it to "3 l milk"`) || strings.Contains(p.Summary, "stored amount") {
		t.Fatalf("summary = %s", p.Summary)
	}
}

func TestGroceryEditNewNameKeepsTheAmount(t *testing.T) {
	for _, c := range []struct{ name, ingredient, quantity, want string }{
		{"2 lemons", "lemons", "2", "2 limes"},
		{"2 lemons", "lemons", "", "2 limes"}, // amount written only in the line
		{"7 Up", "7 up", "", "limes"},
		{"Organic Lemons", "lemons", "2", "2 limes"},
	} {
		t.Run(c.name+"/"+c.quantity, func(t *testing.T) {
			h := newHarness(t, true)
			row := groceryRow(h, lemons)
			row["name"], row["ingredient"], row["quantity"] = c.name, c.ingredient, c.quantity
			h.fake.Put("groceries", row)
			p := h.ok("propose_grocery_edit", map[string]any{"item_uid": lemons, "name": "limes"}).Proposal
			if got := p.GroceryAfter[0].Name; got != c.want {
				t.Fatalf("line = %q, want %q", got, c.want)
			}
		})
	}
}

func TestGroceryEditRefusals(t *testing.T) {
	h := newHarness(t, true)
	for name, args := range map[string]map[string]any{
		"nothing to change": {"item_uid": milk},
		"blank uid":         {"item_uid": " ", "purchased": true},
		"multi-line":        {"item_uid": milk, "name": "milk\nand more"},
	} {
		t.Run(name, func(t *testing.T) {
			h.fails("propose_grocery_edit", args, "invalid_input")
		})
	}
	h.fails("propose_grocery_edit", map[string]any{"item_uid": "NOPE", "purchased": true}, "not_found")
	h.fails("propose_grocery_edit", map[string]any{"item_uid": milk, "aisle": "Bakery"}, "not_found")
	h.fails("propose_grocery_edit", map[string]any{"item_uid": milk, "list_uid": "NOPE"}, "not_found")
	h.fails("propose_grocery_edit", map[string]any{"item_uid": milk, "purchased": false}, "duplicate")      // already so
	h.fails("propose_grocery_edit", map[string]any{"item_uid": milk, "name": "butter"}, "duplicate")        // Butter is on the list
	h.fails("propose_grocery_edit", map[string]any{"item_uid": milk, "list_uid": defaultList}, "duplicate") // it is there already
	h.ok("propose_grocery_edit", map[string]any{"item_uid": milk, "name": "butter", "allow_duplicate": true})
	h.ok("propose_grocery_edit", map[string]any{"item_uid": eggs, "name": "butter", "purchased": true}) // a bought item duplicates nothing

	p := h.ok("propose_grocery_edit", map[string]any{"item_uid": milk, "instruction": "skimmed"}).Proposal
	gone := groceryRow(h, milk)
	gone["deleted"] = true
	h.fake.Put("groceries", gone) // removed in the app
	h.fails("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}, "not_found")
	if h.posts("groceries") != 0 {
		t.Fatal("a refused edit reached Paprika")
	}
}

func TestGroceryEditOfAnItemAlreadyListedTwice(t *testing.T) {
	h := newHarness(t, true)
	second := groceryRow(h, milk)
	second["uid"], second["name"], second["quantity"] = "MILK-AGAIN", "2 l milk", "2 l"
	h.fake.Put("groceries", second)
	// Milk is on the list twice already; changing the amount of one adds no new duplicate.
	p := h.ok("propose_grocery_edit", map[string]any{"item_uid": milk, "quantity": "3 l"}).Proposal
	h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if row := groceryRow(h, milk); row["name"] != "3 l Milk" {
		t.Fatalf("row = %v", row)
	}
	// Renaming it to something else already on the list still is one.
	h.fails("propose_grocery_edit", map[string]any{"item_uid": milk, "name": "butter"}, "duplicate")
}

func TestGroceryCheckOffAndUndo(t *testing.T) {
	h := newHarness(t, true)
	p := h.ok("propose_grocery_check_off", map[string]any{"item_uids": []any{milk, eggs, paperTowels, milk}}).Proposal
	if p.Kind != "grocery_check_off" || len(p.GroceryAfter) != 2 || !p.GroceryAfter[0].Purchased || p.GroceryBefore[0].Purchased ||
		len(p.Skipped) != 1 || p.Skipped[0].ExistingUID != eggs {
		t.Fatalf("proposal = %+v", p)
	}
	if want := "Tick off 2 item(s) as bought on the Paprika grocery list \"My Grocery List\", \"Costco\": Milk; Paper towels."; p.Summary != want {
		t.Fatalf("summary = %s", p.Summary)
	}
	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if !c.Verified || len(c.Items) != 2 || !c.Items[0].Purchased || h.posts("groceries") != 1 {
		t.Fatalf("commit = %+v", c)
	}
	if groceryRow(h, milk)["purchased"] != true || groceryRow(h, paperTowels)["purchased"] != true || groceryRow(h, butter)["purchased"] != false {
		t.Fatal("the wrong items were ticked off")
	}
	if r := h.ok("list_grocery_lists", nil); r.Lists[0].Unpurchased != 2 || r.Lists[1].Unpurchased != 0 {
		t.Fatalf("lists = %+v", r.Lists)
	}

	h.fails("propose_grocery_check_off", map[string]any{"item_uids": []any{milk}}, "duplicate")
	undo := h.ok("propose_grocery_check_off", map[string]any{"item_uids": []any{milk}, "undo": true}).Proposal
	if !strings.HasPrefix(undo.Summary, "Put 1 item(s) back on the Paprika grocery list \"My Grocery List\" as not bought: Milk") {
		t.Fatalf("summary = %s", undo.Summary)
	}
	h.ok("commit_proposal", map[string]any{"proposal_id": undo.ProposalID, "confirm": true})
	if groceryRow(h, milk)["purchased"] != false {
		t.Fatal("undo did not put milk back")
	}
}

func TestGroceryCheckOffUndoRefusesDuplicates(t *testing.T) {
	h := newHarness(t, true)
	twin := groceryRow(h, eggs)
	twin["uid"], twin["purchased"] = "EGGS-AGAIN", false // eggs are on the list again
	h.fake.Put("groceries", twin)
	h.fails("propose_grocery_check_off", map[string]any{"item_uids": []any{eggs}, "undo": true}, "duplicate")
	p := h.ok("propose_grocery_check_off", map[string]any{"item_uids": []any{eggs}, "undo": true, "allow_duplicate": true}).Proposal
	if len(p.GroceryAfter) != 1 {
		t.Fatalf("proposal = %+v", p)
	}

	// Twin added between proposal and commit.
	h = newHarness(t, true)
	tick := h.ok("propose_grocery_check_off", map[string]any{"item_uids": []any{milk, butter}}).Proposal
	h.ok("commit_proposal", map[string]any{"proposal_id": tick.ProposalID, "confirm": true})
	undo := h.ok("propose_grocery_check_off", map[string]any{"item_uids": []any{milk, butter}, "undo": true}).Proposal
	twin = groceryRow(h, milk)
	twin["uid"], twin["purchased"] = "MILK-AGAIN", false
	h.fake.Put("groceries", twin)
	c := h.ok("commit_proposal", map[string]any{"proposal_id": undo.ProposalID, "confirm": true})
	if !c.Verified || len(c.Items) != 1 || c.Items[0].UID != butter || len(c.Skipped) != 1 || c.Skipped[0].ExistingUID != "MILK-AGAIN" {
		t.Fatalf("commit = %+v", c)
	}
	if groceryRow(h, milk)["purchased"] != true {
		t.Fatal("undo put milk back next to its twin")
	}
}

func TestGroceryItemsWithoutIngredientAreNotTwins(t *testing.T) {
	h := newHarness(t, true)
	batteries := groceryRow(h, eggs)
	batteries["uid"], batteries["name"], batteries["ingredient"] = "BATTERIES", "Batteries", ""
	h.fake.Put("groceries", batteries)
	foil := groceryRow(h, butter)
	foil["uid"], foil["name"], foil["ingredient"] = "FOIL", "Foil", ""
	h.fake.Put("groceries", foil)
	h.ok("propose_grocery_check_off", map[string]any{"item_uids": []any{"BATTERIES"}, "undo": true})
	h.ok("propose_grocery_edit", map[string]any{"item_uid": "BATTERIES", "purchased": false})
	h.fails("propose_grocery_edit", map[string]any{"item_uid": "BATTERIES", "purchased": false, "name": "foil"}, "duplicate")
}

func TestGroceryCheckOffUndoOfTwoTwins(t *testing.T) {
	h := newHarness(t, true)
	twin := groceryRow(h, eggs)
	twin["uid"] = "EGGS-AGAIN" // two ticked-off eggs; putting both back would list eggs twice
	h.fake.Put("groceries", twin)
	p := h.ok("propose_grocery_check_off", map[string]any{"item_uids": []any{eggs, "EGGS-AGAIN"}, "undo": true}).Proposal
	if len(p.GroceryAfter) != 1 || len(p.Skipped) != 1 {
		t.Fatalf("proposal = %+v", p)
	}
}

func TestGroceryCheckOffSkipsItemsRemovedMeanwhile(t *testing.T) {
	h := newHarness(t, true)
	p := h.ok("propose_grocery_check_off", map[string]any{"item_uids": []any{milk, butter}}).Proposal
	gone := groceryRow(h, milk)
	gone["deleted"] = true
	h.fake.Put("groceries", gone)
	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if !c.Verified || len(c.Items) != 1 || c.Items[0].UID != butter || len(c.GroceryBefore) != 1 || c.GroceryBefore[0].UID != butter || len(c.Skipped) != 1 || c.Skipped[0].Name != "Milk" {
		t.Fatalf("commit = %+v", c)
	}
	if groceryRow(h, milk) != nil {
		t.Fatal("the check-off brought a removed item back")
	}
}

func TestGroceryRemovalByUIDAndClearPurchased(t *testing.T) {
	h := newHarness(t, true)
	p := h.ok("propose_grocery_removal", map[string]any{"item_uids": []any{butter, paperTowels}}).Proposal
	if p.Kind != "grocery_removal" || len(p.GroceryGone) != 2 {
		t.Fatalf("proposal = %+v", p)
	}
	want := `REMOVE 2 item(s) from the Paprika grocery list "My Grocery List", "Costco": Butter; Paper towels. They disappear from the list on every device.`
	if p.Summary != want {
		t.Fatalf("summary = %s", p.Summary)
	}
	h.fake.DropNextWriteAck()
	h.fails("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}, "upstream_unavailable")
	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if !c.Verified || len(c.GroceryGone) != 2 || h.posts("groceries") != 1 || groceryRow(h, butter) != nil || len(h.fake.Rows("groceries")) != 3 {
		t.Fatalf("commit = %+v posts=%d", c, h.posts("groceries"))
	}

	clear := h.ok("propose_grocery_removal", map[string]any{"purchased_on_list_uid": defaultList}).Proposal
	if len(clear.GroceryGone) != 1 || clear.GroceryGone[0].UID != eggs || !strings.Contains(clear.Summary, "every ticked-off item (Eggs)") {
		t.Fatalf("clear = %+v", clear)
	}
	h.ok("commit_proposal", map[string]any{"proposal_id": clear.ProposalID, "confirm": true})
	if groceryRow(h, eggs) != nil || groceryRow(h, milk) == nil {
		t.Fatal("clearing purchased removed the wrong items")
	}
	h.fails("propose_grocery_removal", map[string]any{"purchased_on_list_uid": defaultList}, "duplicate") // nothing ticked off any more
}

func TestClearPurchasedKeepsItemsPutBackMeanwhile(t *testing.T) {
	h := newHarness(t, true)
	tick := h.ok("propose_grocery_check_off", map[string]any{"item_uids": []any{milk}}).Proposal
	h.ok("commit_proposal", map[string]any{"proposal_id": tick.ProposalID, "confirm": true})
	p := h.ok("propose_grocery_removal", map[string]any{"purchased_on_list_uid": defaultList}).Proposal
	if len(p.GroceryGone) != 2 {
		t.Fatalf("proposal = %+v", p)
	}
	back := groceryRow(h, eggs)
	back["purchased"] = false // needed again, un-ticked in the app
	h.fake.Put("groceries", back)
	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if !c.Verified || len(c.GroceryGone) != 1 || c.GroceryGone[0].UID != milk || len(c.Skipped) != 1 || c.Skipped[0].ExistingUID != eggs {
		t.Fatalf("commit = %+v", c)
	}
	if groceryRow(h, eggs) == nil || groceryRow(h, milk) != nil {
		t.Fatal("clearing purchased removed an item put back on the list")
	}

	tick = h.ok("propose_grocery_check_off", map[string]any{"item_uids": []any{eggs}}).Proposal
	h.ok("commit_proposal", map[string]any{"proposal_id": tick.ProposalID, "confirm": true})
	p = h.ok("propose_grocery_removal", map[string]any{"purchased_on_list_uid": defaultList}).Proposal
	back = groceryRow(h, eggs)
	back["purchased"] = false
	h.fake.Put("groceries", back)
	posts := h.posts("groceries")
	h.fails("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}, "duplicate")
	if h.posts("groceries") != posts || groceryRow(h, eggs) == nil {
		t.Fatal("a clear with every item put back wrote to Paprika")
	}
}

func TestClearPurchasedKeepsItemsMovedMeanwhile(t *testing.T) {
	h := newHarness(t, true)
	p := h.ok("propose_grocery_removal", map[string]any{"purchased_on_list_uid": defaultList}).Proposal
	moved := groceryRow(h, eggs)
	moved["list_uid"] = costcoList // still ticked off, but on a list the clear does not touch
	h.fake.Put("groceries", moved)
	h.fails("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}, "duplicate")
	if groceryRow(h, eggs) == nil || h.posts("groceries") != 0 {
		t.Fatal("clearing one list removed an item moved to another")
	}
}

func TestClearPurchasedKeepsARowItCannotRead(t *testing.T) {
	h := newHarness(t, true)
	p := h.ok("propose_grocery_removal", map[string]any{"purchased_on_list_uid": defaultList}).Proposal
	odd := groceryRow(h, eggs)
	odd["purchased"] = "yes" // a shape the adapter cannot read: whether it is still ticked off is unknown
	h.fake.Put("groceries", odd)
	h.fails("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}, "duplicate")
	if groceryRow(h, eggs) == nil || h.posts("groceries") != 0 {
		t.Fatal("clearing purchased removed a row whose state it could not read")
	}
}

func TestGroceryRemovalInputsMustBeExplicit(t *testing.T) {
	h := newHarness(t, true)
	for name, args := range map[string]map[string]any{
		"neither":   {},
		"both":      {"item_uids": []any{milk}, "purchased_on_list_uid": defaultList},
		"blank uid": {"item_uids": []any{milk, " "}},
	} {
		t.Run(name, func(t *testing.T) {
			h.fails("propose_grocery_removal", args, "invalid_input")
		})
	}
	h.fails("propose_grocery_removal", map[string]any{"item_uids": []any{milk, "NOPE"}}, "not_found")
	h.fails("propose_grocery_removal", map[string]any{"purchased_on_list_uid": "NOPE"}, "not_found")
	if h.posts("groceries") != 0 {
		t.Fatal("a refused removal reached Paprika")
	}
}

func TestGroceryEditMovesToTheEndOfTheListAsItIsAtCommit(t *testing.T) {
	h := newHarness(t, true)
	p := h.ok("propose_grocery_edit", map[string]any{"item_uid": milk, "list_uid": costcoList}).Proposal
	added := groceryRow(h, paperTowels)
	added["uid"], added["name"], added["ingredient"], added["order_flag"] = "C3D1E2F4-0A5B-4C6D-8E7F-9A0B1C2D3E4F", "Napkins", "napkins", float64(1)
	h.fake.Put("groceries", added) // takes the place the proposal saw as the end of Costco
	h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if row := groceryRow(h, milk); row["list_uid"] != costcoList || row["order_flag"] != float64(2) {
		t.Fatalf("row in Paprika = %v", row)
	}
}

func TestGroceryEditWritesNothingWhenTheTargetWasRemoved(t *testing.T) {
	for name, c := range map[string]struct {
		args       map[string]any
		collection string
		uid        string
	}{
		"list":  {map[string]any{"item_uid": milk, "list_uid": costcoList}, "grocerylists", costcoList},
		"aisle": {map[string]any{"item_uid": milk, "aisle": "Baking Goods"}, "groceryaisles", "AFADC523FB4CFA907914740E22016338CBD90DDFA42177B87F9E46F2B376EB28"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t, true)
			p := h.ok("propose_grocery_edit", c.args).Proposal
			var gone map[string]any
			for _, r := range h.fake.Rows(c.collection) {
				if r["uid"] == c.uid {
					gone = r
				}
			}
			gone["deleted"] = true
			h.fake.Put(c.collection, gone)
			h.fails("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}, "not_found")
			if h.posts("groceries") != 0 {
				t.Fatal("the item was written onto a removed " + name)
			}
		})
	}
}

func TestClearPurchasedPostsInBatches(t *testing.T) {
	h := newHarness(t, true)
	base := groceryRow(h, eggs)
	for i := range 30 {
		r := map[string]any{}
		for k, v := range base {
			r[k] = v
		}
		r["uid"], r["name"] = fmt.Sprintf("D0000000-0000-4000-8000-%012d", i), fmt.Sprintf("Bought %d", i)
		h.fake.Put("groceries", r)
	}
	p := h.ok("propose_grocery_removal", map[string]any{"purchased_on_list_uid": defaultList}).Proposal
	if len(p.GroceryGone) != 31 {
		t.Fatalf("proposal = %d items", len(p.GroceryGone))
	}
	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if !c.Verified || len(c.GroceryGone) != 31 || h.posts("groceries") != 2 || h.serverRows("groceries", "purchased", true) != 0 {
		t.Fatalf("commit = %d removed, posts=%d", len(c.GroceryGone), h.posts("groceries"))
	}
}
