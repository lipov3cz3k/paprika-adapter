package paprika

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/url"
)

// RecipeEdit names the recipe fields a caller may overwrite. Nil leaves a field as it is.
type RecipeEdit struct {
	Rating      *int    `json:"rating,omitempty"`
	Notes       *string `json:"notes,omitempty"`
	Description *string `json:"description,omitempty"`
	Directions  *string `json:"directions,omitempty"`
}

// Fields Paprika returns on read but its own client leaves out of a recipe write.
var recipeReadOnly = []string{"photo_url", "on_grocery_list"}

// WriteRecipe overwrites an existing recipe with current plus edit. Paprika
// replaces the whole recipe on write, so current must be the complete object
// as just read from Paprika: every field in it, known to this adapter or not,
// is sent back unchanged apart from the edited ones and the hash, which is new.
// It returns the object that was sent.
func (c *Client) WriteRecipe(ctx context.Context, current Raw, edit RecipeEdit) (json.RawMessage, error) {
	const op = "write recipe"
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(current.JSON, &obj); err != nil || current.UID == "" {
		return nil, &Error{Kind: KindInvalid, Op: op, Message: "current recipe is not a complete Paprika recipe object"}
	}
	for field, v := range map[string]any{"rating": edit.Rating, "notes": edit.Notes, "description": edit.Description, "directions": edit.Directions} {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, &Error{Kind: KindInvalid, Op: op, Message: field + " is not serialisable"}
		}
		if string(raw) != "null" {
			obj[field] = raw
		}
	}
	for _, k := range recipeReadOnly {
		delete(obj, k)
	}
	obj["deleted"] = json.RawMessage("false")
	hash, err := newRecipeHash()
	if err != nil {
		return nil, err
	}
	obj["hash"], _ = json.Marshal(hash)
	body, err := json.Marshal(obj)
	if err != nil {
		return nil, &Error{Kind: KindInvalid, Op: op, Message: "recipe is not serialisable"}
	}
	// A recipe is posted as one object to its own URL, not as an array to a collection URL.
	if err := c.postData(ctx, SyncPath+"recipe/"+url.PathEscape(current.UID)+"/", json.RawMessage(body)); err != nil {
		return nil, err
	}
	return body, nil
}

// newRecipeHash mints the value the Paprika 4 apps put in a recipe's hash after
// every local change: 64 random bits as 16 uppercase hex digits. It says nothing
// about the content; other devices fetch a recipe again when it differs from
// the one they hold.
func newRecipeHash() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", &Error{Kind: KindInvalid, Op: "write recipe", Message: "no random source for the recipe hash", Err: err}
	}
	return fmt.Sprintf("%X", b), nil
}
