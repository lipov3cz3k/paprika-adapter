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
