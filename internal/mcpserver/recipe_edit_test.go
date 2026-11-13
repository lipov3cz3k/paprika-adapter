package mcpserver_test

import (
	"strings"
	"testing"

	"github.com/pavel-alay/paprika-adapter/internal/paprika"
)

const newDirections = "Heat the oven to 220C.\nRoast for 1 hour 20 minutes, then rest."

func recipePosts(h *harness, uid string) int { return h.posts("recipe/" + uid) }

func TestRecipeEditProposeCommitReadBack(t *testing.T) {
	h := newHarness(t, true)
	before := h.fake.Recipe(roastUID)
	p := h.ok("propose_recipe_edit", map[string]any{"uid": roastUID, "rating": 3, "append_note": "Next time: less salt.",
		"description": "Sunday roast.", "directions": newDirections}).Proposal
	if p.Kind != "recipe_edit" || p.Recipe == nil || p.Recipe.Recipe.Name != "Lemon Garlic Roast Chicken" || len(p.Recipe.Changes) != 4 {
		t.Fatalf("proposal = %+v", p)
	}
	if c := p.Recipe.Changes[0]; c.Field != "rating" || c.From != "5" || c.To != "3" {
		t.Fatalf("rating change = %+v", c)
	}
	if c := p.Recipe.Changes[3]; c.Field != "directions" || !strings.HasPrefix(c.From, "Heat the oven to 425F.") || c.To != newDirections {
		t.Fatalf("directions change = %+v", c)
	}
	for _, want := range []string{`"Lemon Garlic Roast Chicken"`, "rating 5 of 5 -> 3 of 5", `add to the notes: "Next time: less salt."`, "replace the directions", "overwrites"} {
		if !strings.Contains(p.Summary, want) {
			t.Fatalf("summary lacks %q: %s", want, p.Summary)
		}
	}
	if recipePosts(h, roastUID) != 0 {
		t.Fatal("proposing wrote to Paprika")
	}

	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if c.Status != "committed" || !c.Verified || c.RecipeEdit == nil || len(c.RecipeEdit.Changes) != 4 || recipePosts(h, roastUID) != 1 {
		t.Fatalf("commit = %+v (posts %d)", c, recipePosts(h, roastUID))
	}
	after := h.fake.Recipe(roastUID)
	if after["rating"] != float64(3) || after["notes"] != "Next time: less salt." || after["description"] != "Sunday roast." || after["directions"] != newDirections {
		t.Fatalf("Paprika holds rating=%v notes=%q description=%q", after["rating"], after["notes"], after["description"])
	}
	for _, field := range []string{"name", "ingredients", "servings", "source", "created", "photo", "photo_hash", "photo_large", "on_favorites", "prep_time"} {
		if after[field] != before[field] {
			t.Errorf("%s changed from %v to %v", field, before[field], after[field])
		}
	}
	if len(after["categories"].([]any)) != len(before["categories"].([]any)) || after["hash"] == before["hash"] {
		t.Fatalf("categories or hash wrong after the edit: %v, %v", after["categories"], after["hash"])
	}

	// The cache already has the new version: no sync needed to read or find it.
	if r := h.ok("get_recipe", map[string]any{"uid": roastUID}).Recipe; r.Rating != 3 || r.Notes != "Next time: less salt." || r.Directions != newDirections {
		t.Fatalf("get_recipe after the edit: rating=%d notes=%q", r.Rating, r.Notes)
	}
	if h.ok("search_recipes", map[string]any{"query": "less salt"}).TotalMatches != 1 {
		t.Fatal("the new note is not searchable")
	}
	if again := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}); again.Status != "already_committed" || recipePosts(h, roastUID) != 1 {
		t.Fatalf("repeat = %+v (posts %d)", again, recipePosts(h, roastUID))
	}
}

func TestAppendNoteKeepsExistingNotesAndIsNotAppliedTwice(t *testing.T) {
	h := newHarness(t, true)
	h.fake.UpdateRecipe(soupUID, "notes", "Good with bread.\n")
	p := h.ok("propose_recipe_edit", map[string]any{"uid": soupUID, "append_note": "Freezes well."}).Proposal
	if c := p.Recipe.Changes[0]; c.Field != "notes" || c.From != "Good with bread.\n" || c.To != "Good with bread.\n\nFreezes well." {
		t.Fatalf("change = %+v", c)
	}
	h.fake.DropNextWriteAck() // Paprika stores the recipe, the response never arrives
	if r := h.fails("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}, "upstream_unavailable"); !r.Error.Retryable {
		t.Fatalf("error = %+v", r.Error)
	}
	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if c.Status != "committed" || !c.Verified || recipePosts(h, soupUID) != 1 || len(c.RecipeEdit.Changes) != 1 {
		t.Fatalf("retry = %+v (posts %d)", c, recipePosts(h, soupUID))
	}
	if notes := h.fake.Recipe(soupUID)["notes"]; notes != "Good with bread.\n\nFreezes well." {
		t.Fatalf("notes = %q", notes)
	}
}

// The user asked for plain overwrite: an edit made elsewhere to the same field
// loses, but edits to other fields survive because the commit starts from a fresh copy.
func TestRecipeEditOverwritesTheFieldAndKeepsWhatElseChanged(t *testing.T) {
	h := newHarness(t, true)
	p := h.ok("propose_recipe_edit", map[string]any{"uid": soupUID, "notes": "Use red lentils only.", "rating": 5}).Proposal
	h.fake.UpdateRecipe(soupUID, "notes", "edited on the phone")
	h.fake.UpdateRecipe(soupUID, "servings", "8")
	h.fake.UpdateRecipe(soupUID, "field_from_a_newer_paprika", "keep me")
	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	after := h.fake.Recipe(soupUID)
	if !c.Verified || after["notes"] != "Use red lentils only." || after["rating"] != float64(5) {
		t.Fatalf("after = notes %q rating %v", after["notes"], after["rating"])
	}
	if after["servings"] != "8" || after["field_from_a_newer_paprika"] != "keep me" {
		t.Fatalf("another field was lost: servings=%v unknown=%v", after["servings"], after["field_from_a_newer_paprika"])
	}
	if _, sent := after["photo_url"]; sent {
		t.Fatal("the signed photo link was written back into the recipe")
	}
}

func TestRecipeEditInputsMustBeExplicit(t *testing.T) {
	h := newHarness(t, true)
	for name, args := range map[string]map[string]any{
		"nothing to change": {"uid": roastUID},
		"blank uid":         {"uid": " ", "rating": 3},
		"six stars":         {"uid": roastUID, "rating": 6},
		"negative stars":    {"uid": roastUID, "rating": -1},
		"replace and add":   {"uid": roastUID, "notes": "a", "append_note": "b"},
		"empty directions":  {"uid": roastUID, "directions": " \n"},
		"too long":          {"uid": roastUID, "notes": strings.Repeat("x", 20001)},
		"in the trash":      {"uid": trashedUID, "rating": 1},
	} {
		t.Run(name, func(t *testing.T) {
			h.fails("propose_recipe_edit", args, "invalid_input")
		})
	}
	h.fails("propose_recipe_edit", map[string]any{"uid": "NOPE", "rating": 3}, "not_found")
	h.fails("propose_recipe_edit", map[string]any{"uid": roastUID, "rating": 5}, "duplicate") // already five stars
	if h.fake.Hits("POST "+paprika.SyncPath+"recipe/") != 0 {
		t.Fatal("a refused edit reached Paprika")
	}
	cleared := h.ok("propose_recipe_edit", map[string]any{"uid": soupUID, "rating": 0}).Proposal
	if !strings.Contains(cleared.Summary, "rating 4 of 5 -> unrated") {
		t.Fatalf("summary = %s", cleared.Summary)
	}
}

func TestRecipeEditIngredientsTimesServingsAndCategories(t *testing.T) {
	h := newHarness(t, true)
	ingredients := "1 whole chicken (about 2 kg)\n2 lemons\n1 head garlic"
	p := h.ok("propose_recipe_edit", map[string]any{"uid": roastUID, "ingredients": ingredients, "prep_time": " 20 mins ", "total_time": "1 hr 35 mins",
		"servings": "6", "source": "", "source_url": "https://example.test/roast", "add_categories": []any{"vegetarian", "Dinner"}}).Proposal
	fields := map[string]string{}
	for _, c := range p.Recipe.Changes {
		fields[c.Field] = c.From + " -> " + c.To
	}
	want := map[string]string{
		"ingredients": h.fake.Recipe(roastUID)["ingredients"].(string) + " -> " + ingredients,
		"prep_time":   "15 mins -> 20 mins", "total_time": " -> 1 hr 35 mins", "servings": "4 to 6 -> 6", "source": "Family -> ",
		"source_url": " -> https://example.test/roast", "categories": "Dinner -> Dinner, Vegetarian",
	}
	if len(fields) != len(want) {
		t.Fatalf("changes = %v", fields)
	}
	for f, w := range want {
		if fields[f] != w {
			t.Errorf("%s: %q, want %q", f, fields[f], w)
		}
	}
	for _, s := range []string{`prep time "15 mins" -> "20 mins"`, "clear the source", "replace the ingredients", "categories Dinner -> Dinner, Vegetarian"} {
		if !strings.Contains(p.Summary, s) {
			t.Errorf("summary lacks %q: %s", s, p.Summary)
		}
	}
	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	after := h.fake.Recipe(roastUID)
	if !c.Verified || after["ingredients"] != ingredients || after["prep_time"] != "20 mins" || after["servings"] != "6" || after["source"] != "" ||
		len(after["categories"].([]any)) != 2 || after["cook_time"] != "1 hr 15 mins" {
		t.Fatalf("after = %v", after)
	}
	r := h.ok("get_recipe", map[string]any{"uid": roastUID}).Recipe
	if len(r.Categories) != 2 || r.IngredientsRaw != ingredients {
		t.Fatalf("get_recipe after the edit = %+v", r)
	}

	// Removing a category works on what the recipe has at commit time.
	p = h.ok("propose_recipe_edit", map[string]any{"uid": roastUID, "remove_categories": []any{"Dinner"}}).Proposal
	h.fake.UpdateRecipe(roastUID, "categories", []any{"2D6BB5F8-909E-4B2B-9E67-9668C2737639", "647E822C-434E-42D4-94A7-C42CC34A0246",
		"9A1F5C3E-7B24-4D10-8E6A-51C0D2A7F3B1"}) // Soup added on the phone meanwhile
	h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if got := h.fake.Recipe(roastUID)["categories"].([]any); len(got) != 2 || got[0] != "647E822C-434E-42D4-94A7-C42CC34A0246" {
		t.Fatalf("categories = %v", got)
	}
	p = h.ok("propose_recipe_edit", map[string]any{"uid": roastUID, "categories": []any{}, "name": "Roast Chicken"}).Proposal
	h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if after := h.fake.Recipe(roastUID); len(after["categories"].([]any)) != 0 || after["name"] != "Roast Chicken" {
		t.Fatalf("after = %v", after)
	}
}

func TestRecipeEditRefusesBadFieldsAndCategories(t *testing.T) {
	h := newHarness(t, true)
	for name, args := range map[string]map[string]any{
		"empty name":           {"uid": roastUID, "name": " "},
		"empty ingredients":    {"uid": roastUID, "ingredients": "\n"},
		"multi-line time":      {"uid": roastUID, "prep_time": "10\nmin"},
		"replace and add":      {"uid": roastUID, "categories": []any{"Dinner"}, "add_categories": []any{"Soup"}},
		"blank category":       {"uid": roastUID, "add_categories": []any{" "}},
		"too long single line": {"uid": roastUID, "servings": strings.Repeat("4", 2001)},
	} {
		t.Run(name, func(t *testing.T) {
			h.fails("propose_recipe_edit", args, "invalid_input")
		})
	}
	h.fails("propose_recipe_edit", map[string]any{"uid": roastUID, "add_categories": []any{"Desserts"}}, "not_found")
	h.fails("propose_recipe_edit", map[string]any{"uid": roastUID, "add_categories": []any{"Dinner"}}, "duplicate")  // already there
	h.fails("propose_recipe_edit", map[string]any{"uid": roastUID, "remove_categories": []any{"Soup"}}, "duplicate") // not there
	h.fails("propose_recipe_edit", map[string]any{"uid": roastUID, "prep_time": "15 mins"}, "duplicate")             // unchanged
	if h.fake.Hits("POST "+paprika.SyncPath+"recipe/"+roastUID+"/") != 0 {
		t.Fatal("a refused edit reached Paprika")
	}
}

// Single-line fields take long links and names in any script; a field of only
// whitespace is written as empty.
func TestRecipeEditTakesLongLinesAndClearsBlankText(t *testing.T) {
	h := newHarness(t, true)
	link := "https://example.com/recipes/roast?" + strings.Repeat("utm_source=x&", 30)
	name := strings.Repeat("Pečené kuře ", 15)
	p := h.ok("propose_recipe_edit", map[string]any{"uid": roastUID, "source_url": link, "name": name, "source": " \t",
		"description": " \n "}).Proposal
	for _, c := range p.Recipe.Changes {
		if c.Field == "description" {
			t.Fatalf("a blank description counts as a change: %+v", c)
		}
	}
	h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	after := h.fake.Recipe(roastUID)
	if after["source_url"] != link || after["name"] != strings.TrimSpace(name) || after["source"] != "" || after["description"] != "" {
		t.Fatalf("Paprika holds source_url=%q name=%q source=%q description=%q", after["source_url"], after["name"], after["source"], after["description"])
	}
}

// Categories that cannot be synced block only an edit that touches categories.
func TestRecipeEditNeedsFreshCategoriesOnlyForCategories(t *testing.T) {
	h := newHarness(t, true)
	h.fake.FailNext("GET "+paprika.SyncPath+"categories/", 500, 500, 500, 500, 500, 500, 500, 500, 500, 500)
	h.ok("propose_recipe_edit", map[string]any{"uid": roastUID, "rating": 2})
	h.fails("propose_recipe_edit", map[string]any{"uid": roastUID, "add_categories": []any{"Soup"}}, "upstream_unavailable")
}
