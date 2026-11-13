package paprika

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// RecipeEdit names the recipe fields a caller may overwrite. Nil, or a text
// field absent from Text, leaves a field as it is.
type RecipeEdit struct {
	Rating     *int              `json:"rating,omitempty"`
	Text       map[string]string `json:"text,omitempty"`       // one of RecipeTextFields -> its new value
	Categories *[]string         `json:"categories,omitempty"` // category uids; replaces the whole list
	InTrash    *bool             `json:"in_trash,omitempty"`   // true moves the recipe to Paprika's trash, false takes it out
}

// RecipeTextFields are the recipe's text fields a RecipeEdit may overwrite, in the order edits list them.
var RecipeTextFields = []string{
	"notes", "description", "directions", "name", "ingredients", "servings",
	"prep_time", "cook_time", "total_time", "difficulty", "source", "source_url", "nutritional_info",
}

// structuredFrom lists, for a text field, the numbers Paprika 4 keeps beside it.
// The apps show and parse the text; the numbers would go stale when the text
// changes, so an edit of the text clears them, as recipes from older apps have them.
var structuredFrom = map[string][]string{
	"prep_time": {"prep_minutes"}, "cook_time": {"cook_minutes"}, "total_time": {"total_minutes"},
	"servings": {"servings_min", "servings_max"},
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
	set := map[string]any{"rating": edit.Rating, "categories": edit.Categories, "in_trash": edit.InTrash}
	for field, v := range edit.Text {
		if !slices.Contains(RecipeTextFields, field) {
			return nil, &Error{Kind: KindInvalid, Op: op, Message: field + " is not an editable recipe field"}
		}
		set[field] = v
		for _, n := range structuredFrom[field] {
			if old, ok := obj[n]; ok && string(old) != "null" && v != textOf(obj[field]) {
				obj[n] = json.RawMessage("null")
			}
		}
	}
	for field, v := range set {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, &Error{Kind: KindInvalid, Op: op, Message: field + " is not serialisable"}
		}
		if string(raw) != "null" {
			obj[field] = raw
		}
	}
	return c.postRecipe(ctx, op, current.UID, obj)
}

// NewRecipe is a recipe to create. Text holds any of RecipeTextFields; the rest are "".
type NewRecipe struct {
	UID        string            `json:"uid"`
	Text       map[string]string `json:"text"`
	Rating     int               `json:"rating"`
	Categories []string          `json:"categories"` // category uids
	Created    string            `json:"created"`    // "YYYY-MM-DD HH:MM:SS", local time as the apps write it
}

// CreateRecipe posts a new recipe in the shape the Paprika 4 apps save one that
// has no derived metadata: the 27 fields Paprika 3 sent plus cookbook_uid null
// (the default cookbook), photo and scale empty, not pinned, not a favourite.
// Without metadata_version the apps derive the *_minutes and servings_* numbers
// from the text themselves. The caller must have checked that r.UID is not in
// use: Paprika replaces by uid. It returns the object that was sent.
func (c *Client) CreateRecipe(ctx context.Context, r NewRecipe) (json.RawMessage, error) {
	const op = "create recipe"
	if err := checkNewUID(op, r.UID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(r.Text["name"]) == "" {
		return nil, &Error{Kind: KindInvalid, Op: op, Message: "a recipe needs a name"}
	}
	categories := r.Categories
	if categories == nil {
		categories = []string{}
	}
	fields := map[string]any{
		"uid": r.UID, "rating": r.Rating, "categories": categories, "created": r.Created,
		"image_url": nil, "photo": nil, "photo_hash": nil, "photo_large": nil, "scale": nil, "hash": "",
		"in_trash": false, "is_pinned": false, "on_favorites": false, "cookbook_uid": nil,
	}
	for _, f := range RecipeTextFields {
		fields[f] = r.Text[f]
	}
	for f := range r.Text {
		if !slices.Contains(RecipeTextFields, f) {
			return nil, &Error{Kind: KindInvalid, Op: op, Message: f + " is not a recipe text field"}
		}
	}
	obj := make(map[string]json.RawMessage, len(fields)+1)
	for k, v := range fields {
		raw, err := json.Marshal(v)
		if err != nil {
			return nil, &Error{Kind: KindInvalid, Op: op, Message: k + " is not serialisable"}
		}
		obj[k] = raw
	}
	return c.postRecipe(ctx, op, r.UID, obj)
}

// postRecipe stamps a new hash on a whole recipe object and posts it to the
// recipe's own URL, the way the apps save one. Every write gets a new hash, so
// the apps notice any change, a move into or out of the trash included.
func (c *Client) postRecipe(ctx context.Context, op, uid string, obj map[string]json.RawMessage) (json.RawMessage, error) {
	for _, k := range recipeReadOnly {
		delete(obj, k)
	}
	obj["deleted"] = json.RawMessage("false")
	hash, err := newRecipeHash(op)
	if err != nil {
		return nil, err
	}
	obj["hash"], _ = json.Marshal(hash)
	body, err := json.Marshal(obj)
	if err != nil {
		return nil, &Error{Kind: KindInvalid, Op: op, Message: "recipe is not serialisable"}
	}
	// A recipe is posted as one object to its own URL, not as an array to a collection URL.
	if err := c.postData(ctx, SyncPath+"recipe/"+url.PathEscape(uid)+"/", json.RawMessage(body)); err != nil {
		return nil, err
	}
	return body, nil
}

// newRecipeHash mints the value the Paprika 4 apps put in a recipe's hash after
// every local change: 64 random bits as 16 uppercase hex digits. It says nothing
// about the content; other devices fetch a recipe again when it differs from
// the one they hold.
func newRecipeHash(op string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", &Error{Kind: KindInvalid, Op: op, Message: "no random source for the recipe hash", Err: err}
	}
	return fmt.Sprintf("%X", b), nil
}

// textOf decodes a JSON string value, "" for anything else.
func textOf(raw json.RawMessage) string {
	var s string
	_ = json.Unmarshal(raw, &s)
	return s
}
