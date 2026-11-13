package paprika_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/pavel-alay/paprika-adapter/internal/paprika"
)

func TestWriteRecipeSendsTheWholeRecipeBack(t *testing.T) {
	const uid = "014031E3-BC74-4CA8-B345-36EBE9C9794B"
	fake := newFake(t)
	fake.UpdateRecipe(uid, "field_from_a_newer_paprika", map[string]any{"keep": "me"})
	c := newClient(t, fake)
	ctx := context.Background()
	current, err := c.Recipe(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	before := fake.Recipe(uid)
	rating, notes := 2, "Line one.\nLine two / with a slash."
	sent, err := c.WriteRecipe(ctx, current, paprika.RecipeEdit{Rating: &rating, Text: map[string]string{"notes": notes}})
	if err != nil {
		t.Fatal(err)
	}
	if fake.Hits("POST "+paprika.SyncPath+"recipe/"+uid+"/") != 1 || fake.Hits("POST "+paprika.SyncPath+"recipes/") != 0 {
		t.Fatal("the recipe was not posted once to its own URL")
	}
	var body map[string]any
	if err := json.Unmarshal(sent, &body); err != nil {
		t.Fatal(err)
	}
	// Paprika 4 stamps 64 random bits on every change; the fake refuses any other shape.
	if hash, _ := body["hash"].(string); len(hash) != 16 || hash == before["hash"] || hash != fake.Recipe(uid)["hash"] {
		t.Fatalf("stamped hash %v (was %v, stored %v), want a new 16-digit one", body["hash"], before["hash"], fake.Recipe(uid)["hash"])
	}
	again, err := c.WriteRecipe(ctx, current, paprika.RecipeEdit{Rating: &rating, Text: map[string]string{"notes": notes}})
	if err != nil {
		t.Fatal(err)
	}
	if string(again) == string(sent) {
		t.Fatal("the same edit sent twice carried the same hash")
	}
	if body["deleted"] != false || body["rating"] != float64(2) || body["notes"] != notes {
		t.Fatalf("sent rating=%v notes=%q deleted=%v", body["rating"], body["notes"], body["deleted"])
	}
	for _, readOnly := range []string{"photo_url", "on_grocery_list"} {
		if _, ok := body[readOnly]; ok {
			t.Errorf("%s was sent back", readOnly)
		}
	}
	for field, old := range before {
		switch field {
		case "rating", "notes", "hash", "on_grocery_list":
			continue
		}
		oldJSON, _ := json.Marshal(old)
		newJSON, _ := json.Marshal(body[field])
		if string(oldJSON) != string(newJSON) {
			t.Errorf("%s changed from %s to %s", field, oldJSON, newJSON)
		}
	}
	_, err = c.WriteRecipe(ctx, paprika.Raw{UID: uid, JSON: []byte("[]")}, paprika.RecipeEdit{Rating: &rating})
	wantKind(t, err, paprika.KindInvalid)
}

// Paprika 4 keeps minutes and a servings range beside the text. The apps show the
// text, so an edit of the text clears the numbers instead of leaving them stale.
func TestWriteRecipeClearsTheNumbersBesideAnEditedText(t *testing.T) {
	const uid = "014031E3-BC74-4CA8-B345-36EBE9C9794B"
	fake := newFake(t)
	for field, v := range map[string]any{"prep_minutes": 15, "cook_minutes": 75, "total_minutes": 90, "servings_min": 4, "servings_max": 6} {
		fake.UpdateRecipe(uid, field, v)
	}
	c := newClient(t, fake)
	ctx := context.Background()
	current, err := c.Recipe(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	text := map[string]string{"prep_time": "20 min", "cook_time": "1 hr 15 mins", "servings": "8"} // cook_time as it already is
	sent, err := c.WriteRecipe(ctx, current, paprika.RecipeEdit{Text: text})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(sent, &body)
	if body["prep_minutes"] != nil || body["servings_min"] != nil || body["servings_max"] != nil {
		t.Fatalf("stale numbers sent: prep %v servings %v-%v", body["prep_minutes"], body["servings_min"], body["servings_max"])
	}
	if body["cook_minutes"] != float64(75) || body["total_minutes"] != float64(90) {
		t.Fatalf("numbers of unchanged text were cleared: cook %v total %v", body["cook_minutes"], body["total_minutes"])
	}
	// A recipe from an older app has the numbers null or not at all, and keeps it so.
	const soup = "DF8BD763-2ADA-49E2-AA2C-05A47A81B007-6735-000003ABB738847D"
	_, hadKey := fake.Recipe(soup)["prep_minutes"]
	plain, _ := c.Recipe(ctx, soup)
	sent, _ = c.WriteRecipe(ctx, plain, paprika.RecipeEdit{Text: text})
	body = nil
	_ = json.Unmarshal(sent, &body)
	if v, has := body["prep_minutes"]; has != hadKey || v != nil {
		t.Fatalf("prep_minutes of a recipe without numbers became %v (present %v, was %v)", v, has, hadKey)
	}
}

// Trashing must change the hash, or the apps never notice it. Every write
// stamps a new one, so moving a recipe into the trash and out again does.
func TestTrashingARecipeChangesItsHash(t *testing.T) {
	const uid = "014031E3-BC74-4CA8-B345-36EBE9C9794B"
	fake := newFake(t)
	c := newClient(t, fake)
	ctx := context.Background()
	write := func(edit paprika.RecipeEdit) map[string]any {
		t.Helper()
		cur, err := c.Recipe(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		sent, err := c.WriteRecipe(ctx, cur, edit)
		if err != nil {
			t.Fatal(err)
		}
		var body map[string]any
		_ = json.Unmarshal(sent, &body)
		return body
	}
	before := fake.Recipe(uid)["hash"]
	yes, no := true, false
	trashed := write(paprika.RecipeEdit{InTrash: &yes})
	restored := write(paprika.RecipeEdit{InTrash: &no})
	if trashed["in_trash"] != true || restored["in_trash"] != false {
		t.Fatalf("in_trash trashed %v, restored %v", trashed["in_trash"], restored["in_trash"])
	}
	if trashed["hash"] == before || restored["hash"] == trashed["hash"] || restored["hash"] == before {
		t.Fatalf("hash before %v, trashed %v, restored %v: every move must change it", before, trashed["hash"], restored["hash"])
	}
}
