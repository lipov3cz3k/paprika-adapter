package mcpserver_test

import (
	"strings"
	"testing"

	"github.com/pavel-alay/paprika-adapter/internal/paprika"
)

func TestRecipeCreateProposeCommitReadBack(t *testing.T) {
	h := newHarness(t, true)
	ingredients := "Dough:\n500 g flour\n7 g yeast\n300 ml water"
	p := h.ok("propose_recipe_create", map[string]any{"name": " Pizza dough ", "ingredients": ingredients, "directions": "Mix.\nRest 2 hours.",
		"servings": "4", "prep_time": "15 min", "source": "Grandma", "categories": []any{"baking", "Dinner"}, "rating": 4}).Proposal
	if p.Kind != "recipe_create" || p.RecipeNew == nil || p.RecipeNew.Recipe.Name != "Pizza dough" || p.RecipeNew.Recipe.UID == "" {
		t.Fatalf("proposal = %+v", p)
	}
	for _, s := range []string{`Create the Paprika recipe "Pizza dough"`, "rating 4 of 5", "ingredients (4 lines)", "prep time 15 min", "categories Baking, Dinner"} {
		if !strings.Contains(p.Summary, s) {
			t.Errorf("summary lacks %q: %s", s, p.Summary)
		}
	}
	uid := p.RecipeNew.Recipe.UID
	if recipePosts(h, uid) != 0 {
		t.Fatal("proposing wrote to Paprika")
	}

	h.fake.DropNextWriteAck()
	h.fails("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}, "upstream_unavailable")
	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if c.Status != "committed" || !c.Verified || c.RecipeNew == nil || c.RecipeNew.Recipe.UID != uid || recipePosts(h, uid) != 1 {
		t.Fatalf("commit = %+v (posts %d)", c, recipePosts(h, uid))
	}
	got := h.fake.Recipe(uid)
	if got["name"] != "Pizza dough" || got["ingredients"] != ingredients || got["servings"] != "4" || got["rating"] != float64(4) ||
		got["in_trash"] != false || got["created"] != "2026-10-08 12:00:00" || len(got["categories"].([]any)) != 2 || len(got["hash"].(string)) != 16 {
		t.Fatalf("recipe in Paprika = %v", got)
	}
	// Paprika 4 sends the default cookbook as null and leaves the derived numbers to the apps.
	if v, ok := got["cookbook_uid"]; !ok || v != nil {
		t.Fatalf("cookbook_uid = %v (sent %v), want null", v, ok)
	}
	if _, ok := got["metadata_version"]; ok {
		t.Fatal("metadata_version was sent")
	}
	// The cache holds it straight away: it can be read, found and edited without a sync.
	if r := h.ok("get_recipe", map[string]any{"uid": uid}).Recipe; r.Name != "Pizza dough" || len(r.Categories) != 2 {
		t.Fatalf("get_recipe = %+v", r)
	}
	if !found(h.ok("search_recipes", map[string]any{"query": "yeast pizza"}), uid) {
		t.Fatal("the new recipe is not searchable")
	}
	h.ok("propose_recipe_edit", map[string]any{"uid": uid, "cook_time": "12 min"})
	h.fails("propose_recipe_create", map[string]any{"name": "pizza DOUGH"}, "duplicate")
	if again := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}); again.Status != "already_committed" || recipePosts(h, uid) != 1 {
		t.Fatalf("repeat = %+v", again)
	}
}

// The commit checks every field it gave the recipe, not only name and texts.
func TestRecipeCreateVerifiesEveryField(t *testing.T) {
	h := newHarness(t, true)
	p := h.ok("propose_recipe_create", map[string]any{"name": "Flatbread", "ingredients": "flour\nwater", "directions": "Bake.",
		"rating": 4, "categories": []any{"Baking"}}).Proposal
	uid := p.RecipeNew.Recipe.UID
	h.fake.DropNextWriteAck()
	h.fails("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}, "upstream_unavailable")
	h.fake.UpdateRecipe(uid, "rating", 2)
	h.fails("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}, "write_not_verified")
	h.fake.UpdateRecipe(uid, "rating", 4)
	h.fake.UpdateRecipe(uid, "categories", []any{})
	h.fails("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}, "write_not_verified")
	if recipePosts(h, uid) != 1 {
		t.Fatalf("posts = %d, want 1", recipePosts(h, uid))
	}
}

func TestRecipeCreateRefusals(t *testing.T) {
	h := newHarness(t, true)
	for name, args := range map[string]map[string]any{
		"blank name":      {"name": "  "},
		"multi-line name": {"name": "Soup\nand more"},
		"bad rating":      {"name": "Soup 2", "rating": 6},
	} {
		t.Run(name, func(t *testing.T) {
			h.fails("propose_recipe_create", args, "invalid_input")
		})
	}
	h.fails("propose_recipe_create", map[string]any{"name": "Pie", "categories": []any{"Desserts"}}, "not_found")
	h.fails("propose_recipe_create", map[string]any{"name": "lemon garlic roast chicken"}, "duplicate")
	if p := h.ok("propose_recipe_create", map[string]any{"name": "Lemon Garlic Roast Chicken", "allow_duplicate": true}).Proposal; p.Summary != `Create the Paprika recipe "Lemon Garlic Roast Chicken".` {
		t.Fatalf("summary = %s", p.Summary)
	}
}

func TestRecipeTrashAndRestore(t *testing.T) {
	h := newHarness(t, true)
	p := h.ok("propose_recipe_trash", map[string]any{"uids": []any{roastUID, trashedUID, roastUID}}).Proposal
	if p.Kind != "recipe_trash" || len(p.RecipesTrashed) != 1 || len(p.Skipped) != 1 || p.Skipped[0].ExistingUID != trashedUID {
		t.Fatalf("proposal = %+v", p)
	}
	if !strings.HasPrefix(p.Summary, `Move 1 recipe(s) to the Paprika trash: "Lemon Garlic Roast Chicken".`) {
		t.Fatalf("summary = %s", p.Summary)
	}
	before := h.fake.Recipe(roastUID)
	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	after := h.fake.Recipe(roastUID)
	if !c.Verified || len(c.Trashed) != 1 || after["in_trash"] != true || after["ingredients"] != before["ingredients"] || recipePosts(h, roastUID) != 1 {
		t.Fatalf("commit = %+v in_trash=%v", c, after["in_trash"])
	}
	if r := h.ok("search_recipes", map[string]any{"query": "lemon"}); found(r, roastUID) {
		t.Fatalf("a trashed recipe is still found: %+v", r.Recipes)
	}
	h.fails("propose_recipe_edit", map[string]any{"uid": roastUID, "rating": 1}, "invalid_input") // trashed recipes are not edited
	h.fails("propose_recipe_trash", map[string]any{"uids": []any{roastUID}}, "duplicate")

	back := h.ok("propose_recipe_trash", map[string]any{"uids": []any{roastUID}, "restore": true}).Proposal
	h.ok("commit_proposal", map[string]any{"proposal_id": back.ProposalID, "confirm": true})
	if h.fake.Recipe(roastUID)["in_trash"] != false || !found(h.ok("search_recipes", map[string]any{"query": "lemon"}), roastUID) {
		t.Fatal("restore did not take the recipe out of the trash")
	}
	for name, args := range map[string]map[string]any{"no uids": {"uids": []any{}}, "blank uid": {"uids": []any{" "}}} {
		t.Run(name, func(t *testing.T) { h.fails("propose_recipe_trash", args, "invalid_input") })
	}
	h.fails("propose_recipe_trash", map[string]any{"uids": []any{"NOPE"}}, "not_found")
}

func found(r reply, uid string) bool {
	for _, rec := range r.Recipes {
		if rec.UID == uid {
			return true
		}
	}
	return false
}

// Categories that cannot be synced block only a recipe created with categories.
func TestRecipeCreateNeedsFreshCategoriesOnlyForCategories(t *testing.T) {
	h := newHarness(t, true)
	h.fake.FailNext("GET "+paprika.SyncPath+"categories/", 500, 500, 500, 500, 500, 500, 500, 500, 500, 500)
	h.ok("propose_recipe_create", map[string]any{"name": "Plain Toast"})
	h.fails("propose_recipe_create", map[string]any{"name": "Soup Toast", "categories": []any{"Soup"}}, "upstream_unavailable")
}
