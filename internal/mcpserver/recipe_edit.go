package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/pavel-alay/paprika-adapter/internal/paprika"
	"github.com/pavel-alay/paprika-adapter/internal/store"
)

const (
	maxRecipeText = 20000
	maxRecipeLine = 2000 // a single-line field such as name or source_url; long links are common
)

type ProposeRecipeEditIn struct {
	UID              string    `json:"uid" jsonschema:"recipe uid from search_recipes"`
	Rating           *int      `json:"rating,omitempty" jsonschema:"stars, 1 to 5; 0 removes the rating"`
	Name             *string   `json:"name,omitempty" jsonschema:"rename the recipe; cannot be empty"`
	Ingredients      *string   `json:"ingredients,omitempty" jsonschema:"REPLACES the ingredient list in full, one ingredient per line, as Paprika stores it (e.g. 200 g flour); cannot be empty"`
	Directions       *string   `json:"directions,omitempty" jsonschema:"REPLACES the recipe's directions in full, one step per line; cannot be empty"`
	Description      *string   `json:"description,omitempty" jsonschema:"REPLACES the recipe's description; an empty string clears it"`
	Notes            *string   `json:"notes,omitempty" jsonschema:"REPLACES the recipe's notes; an empty string clears them. To add to the notes use append_note instead"`
	AppendNote       string    `json:"append_note,omitempty" jsonschema:"text added as a new paragraph after the existing notes; cannot be combined with notes"`
	NutritionalInfo  *string   `json:"nutritional_info,omitempty" jsonschema:"REPLACES the nutrition text; an empty string clears it"`
	Servings         *string   `json:"servings,omitempty" jsonschema:"servings as free text, e.g. 4 or 4-6 portions; an empty string clears it"`
	PrepTime         *string   `json:"prep_time,omitempty" jsonschema:"preparation time as free text; write it in minutes, e.g. 15 min or 90 min, which the apps can parse back; an empty string clears it"`
	CookTime         *string   `json:"cook_time,omitempty" jsonschema:"cooking time as free text in minutes, e.g. 80 min; an empty string clears it"`
	TotalTime        *string   `json:"total_time,omitempty" jsonschema:"total time as free text in minutes, e.g. 95 min; an empty string clears it"`
	Difficulty       *string   `json:"difficulty,omitempty" jsonschema:"difficulty as free text, e.g. Easy; an empty string clears it"`
	Source           *string   `json:"source,omitempty" jsonschema:"where the recipe comes from, e.g. a book or site name; an empty string clears it"`
	SourceURL        *string   `json:"source_url,omitempty" jsonschema:"link to the original recipe; an empty string clears it"`
	Categories       *[]string `json:"categories,omitempty" jsonschema:"REPLACES the recipe's categories with these, by name; they must exist in Paprika; an empty list removes all. Not together with add_categories or remove_categories"`
	AddCategories    []string  `json:"add_categories,omitempty" jsonschema:"put the recipe in these categories too, by name; they must exist in Paprika"`
	RemoveCategories []string  `json:"remove_categories,omitempty" jsonschema:"take the recipe out of these categories, by name"`
}

// recipeEdit is what a proposal stores. The note to append and the categories
// to add or remove are kept apart from Edit because they are applied to what the
// recipe has at commit time.
type recipeEdit struct {
	UID              string             `json:"uid"`
	Name             string             `json:"name"`
	Edit             paprika.RecipeEdit `json:"edit"`
	AppendNote       string             `json:"append_note,omitempty"`
	AddCategories    []string           `json:"add_categories,omitempty"`    // uids
	RemoveCategories []string           `json:"remove_categories,omitempty"` // uids
	Proposed         []FieldChange      `json:"proposed"`                    // as shown to the user, with the values the recipe had then
}

// UnmarshalJSON also reads a proposal stored by an earlier version, which kept
// notes, description and directions directly under edit rather than in
// edit.text. Without this such a proposal would decode as an edit of nothing,
// and its commit would report the proposed change as already made.
func (e *recipeEdit) UnmarshalJSON(data []byte) error {
	type plain recipeEdit
	var aux struct {
		plain
		Legacy struct {
			Notes       *string `json:"notes"`
			Description *string `json:"description"`
			Directions  *string `json:"directions"`
		} `json:"edit"`
	}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}
	if err := json.Unmarshal(data, &aux.plain); err != nil {
		return err
	}
	*e = recipeEdit(aux.plain)
	for field, v := range map[string]*string{"notes": aux.Legacy.Notes, "description": aux.Legacy.Description,
		"directions": aux.Legacy.Directions} {
		if v == nil {
			continue
		}
		if e.Edit.Text == nil {
			e.Edit.Text = map[string]string{}
		}
		if _, ok := e.Edit.Text[field]; !ok {
			e.Edit.Text[field] = *v
		}
	}
	return nil
}

type RecipeEditView struct {
	Recipe  NamedRef      `json:"recipe"`
	Changes []FieldChange `json:"changes" jsonschema:"one entry per field that changes"`
}

type FieldChange struct {
	Field string `json:"field" jsonschema:"rating, categories, or the name of a text field such as ingredients or prep_time"`
	From  string `json:"from" jsonschema:"value before the change; a rating is given as a number, 0 = unrated; categories as names separated by commas"`
	To    string `json:"to" jsonschema:"value after the change"`
}

// singleLine are the text fields that hold one line, and must not be empty when edited.
var (
	singleLine = map[string]bool{"name": true, "servings": true, "prep_time": true, "cook_time": true, "total_time": true,
		"difficulty": true, "source": true, "source_url": true}
	required = map[string]bool{"name": true, "ingredients": true, "directions": true}
)

func (s *server) proposeRecipeEdit(ctx context.Context, in ProposeRecipeEditIn, out *ProposeOut) *ToolError {
	uid := strings.TrimSpace(in.UID)
	if uid == "" {
		return invalid("uid is required")
	}
	edit := recipeEdit{UID: uid, Edit: paprika.RecipeEdit{Rating: in.Rating, Text: map[string]string{}},
		AppendNote: strings.TrimSpace(strings.ReplaceAll(in.AppendNote, "\r\n", "\n"))}
	if in.Rating != nil && (*in.Rating < 0 || *in.Rating > 5) {
		return invalid("rating must be 0 to 5")
	}
	if in.Notes != nil && edit.AppendNote != "" {
		return invalid("give notes (replace) or append_note (add), not both")
	}
	if in.Categories != nil && (len(in.AddCategories) > 0 || len(in.RemoveCategories) > 0) {
		return invalid("give categories (replace) or add_categories/remove_categories, not both")
	}
	given := map[string]*string{"name": in.Name, "ingredients": in.Ingredients, "directions": in.Directions, "description": in.Description,
		"notes": in.Notes, "nutritional_info": in.NutritionalInfo, "servings": in.Servings, "prep_time": in.PrepTime, "cook_time": in.CookTime,
		"total_time": in.TotalTime, "difficulty": in.Difficulty, "source": in.Source, "source_url": in.SourceURL}
	text, terr := recipeTextInput(given, required)
	if terr != nil {
		return terr
	}
	edit.Edit.Text = text
	if terr := checkRecipeText("append_note", edit.AppendNote); terr != nil {
		return terr
	}
	if edit.Edit.Rating == nil && len(edit.Edit.Text) == 0 && edit.AppendNote == "" && in.Categories == nil &&
		len(in.AddCategories) == 0 && len(in.RemoveCategories) == 0 {
		return invalid("give at least one field to change: rating, a text field such as ingredients or prep_time, append_note, or categories")
	}
	fresh := []string{paprika.Recipes}
	if in.Categories != nil || len(in.AddCategories) > 0 || len(in.RemoveCategories) > 0 {
		fresh = append(fresh, string(paprika.Categories))
	}
	if terr := s.ensureFresh(ctx, &out.Meta, fresh...); terr != nil {
		return terr
	}
	cats, terr := s.categoryIndex(ctx)
	if terr != nil {
		return terr
	}
	if in.Categories != nil {
		uids, terr := cats.resolve(*in.Categories)
		if terr != nil {
			return terr
		}
		edit.Edit.Categories = &uids
	}
	if edit.AddCategories, terr = cats.resolve(in.AddCategories); terr != nil {
		return terr
	}
	if edit.RemoveCategories, terr = cats.resolve(in.RemoveCategories); terr != nil {
		return terr
	}
	cached, ok, err := s.Store.Recipe(ctx, uid)
	if err != nil {
		return fromErr(err)
	}
	if !ok {
		return notFound("no recipe with uid " + uid)
	}
	recipe, err := paprika.Decode[paprika.Recipe](cached.Raw)
	if err != nil {
		return fromErr(err)
	}
	if recipe.InTrash {
		return invalid("recipe " + recipe.Name + " is in Paprika's trash")
	}
	edit.Name = recipe.Name
	changes := edit.changes(recipe, cats.names)
	if len(changes) == 0 {
		return &ToolError{Code: "duplicate", Message: fmt.Sprintf("nothing to change: recipe %q already has these values", recipe.Name)}
	}
	var parts []string
	for _, c := range changes {
		label := strings.ReplaceAll(c.Field, "_", " ")
		switch {
		case c.Field == "rating":
			parts = append(parts, fmt.Sprintf("rating %s -> %s", stars(c.From), stars(c.To)))
		case c.Field == "notes" && edit.AppendNote != "":
			parts = append(parts, fmt.Sprintf("add to the notes: %s", preview(edit.AppendNote)))
		case c.Field == "categories":
			parts = append(parts, fmt.Sprintf("categories %s -> %s", orNone(c.From), orNone(c.To)))
		case c.To == "":
			parts = append(parts, fmt.Sprintf("clear the %s (%d characters)", label, utf8.RuneCountInString(c.From)))
		case singleLine[c.Field]:
			parts = append(parts, fmt.Sprintf("%s %q -> %q", label, c.From, c.To))
		default:
			parts = append(parts, fmt.Sprintf("replace the %s (%d characters) with: %s", label, utf8.RuneCountInString(c.From), preview(c.To)))
		}
	}
	edit.Proposed = changes
	p := payload{Recipe: &edit, Summary: fmt.Sprintf("Change the Paprika recipe %q: %s. This overwrites what the recipe has now.", recipe.Name, strings.Join(parts, "; "))}
	view, terr := s.saveProposal(ctx, kindRecipe, p)
	if terr != nil {
		return terr
	}
	view.Recipe = &RecipeEditView{Recipe: NamedRef{UID: uid, Name: recipe.Name}, Changes: changes}
	out.Proposal = view
	return nil
}

// commitRecipeEdit reads the recipe from Paprika, overwrites the edited fields
// on that copy and reads it back. A change made elsewhere to the same field is
// overwritten without warning: that is what the user asked for.
func (s *server) commitRecipeEdit(ctx context.Context, p payload, out *CommitOut) *ToolError {
	edit := *p.Recipe
	cats, terr := s.categoryIndex(ctx)
	if terr != nil {
		return terr
	}
	current, err := s.Client.Recipe(ctx, edit.UID)
	if err != nil {
		return fromErr(err)
	}
	before, err := paprika.Decode[paprika.Recipe](current.JSON)
	if err != nil {
		return fromErr(err)
	}
	changes := edit.changes(before, cats.names)
	after, stored := before, current
	if len(changes) == 0 { // an earlier attempt already landed; report what was proposed
		changes = append(changes, edit.Proposed...)
	} else {
		if _, err := s.Client.WriteRecipe(ctx, current, edit.resolved(before)); err != nil {
			return writeFailed(err)
		}
		if stored, err = s.Client.Recipe(ctx, edit.UID); err != nil {
			return notVerified(fromErr(err).Message)
		}
		if after, err = paprika.Decode[paprika.Recipe](stored.JSON); err != nil {
			return fromErr(err)
		}
		if left := edit.changes(after, cats.names); len(left) > 0 {
			return notVerified("Paprika accepted the recipe but still returns the old " + left[0].Field)
		}
	}
	if err := s.Store.PutRecipe(ctx, stored, after.Hash, s.Now()); err != nil {
		return fromErr(err)
	}
	for i := range changes {
		changes[i].To = fieldValue(after, changes[i].Field, cats.names)
	}
	out.Recipe = &RecipeEditView{Recipe: NamedRef{UID: after.UID, Name: after.Name}, Changes: changes}
	out.Sync = s.Sync.Freshness(ctx, paprika.Recipes)
	return nil
}

// resolved turns the edit into plain field values for the recipe as it is now.
func (e recipeEdit) resolved(now paprika.Recipe) paprika.RecipeEdit {
	out := e.Edit
	out.Text = maps.Clone(e.Edit.Text)
	if e.AppendNote != "" {
		notes := strings.TrimRight(now.Notes, " \t\r\n")
		switch {
		case notes == "":
			notes = e.AppendNote
		case notes != e.AppendNote && !strings.HasSuffix(notes, "\n\n"+e.AppendNote): // not added yet
			notes += "\n\n" + e.AppendNote
		default:
			notes = now.Notes
		}
		if out.Text == nil {
			out.Text = map[string]string{}
		}
		out.Text["notes"] = notes
	}
	if len(e.AddCategories) > 0 || len(e.RemoveCategories) > 0 {
		uids := []string{}
		for _, u := range now.Categories {
			if !slices.Contains(e.RemoveCategories, u) {
				uids = append(uids, u)
			}
		}
		for _, u := range e.AddCategories {
			if !slices.Contains(uids, u) {
				uids = append(uids, u)
			}
		}
		out.Categories = &uids
	}
	return out
}

// changes lists the fields whose value in now differs from what the edit wants.
func (e recipeEdit) changes(now paprika.Recipe, catNames map[string]string) []FieldChange {
	want := e.resolved(now)
	var out []FieldChange
	if want.Rating != nil && *want.Rating != now.Rating {
		out = append(out, FieldChange{Field: "rating", From: fmt.Sprint(now.Rating), To: fmt.Sprint(*want.Rating)})
	}
	text := recipeText(now)
	for _, field := range paprika.RecipeTextFields {
		if to, ok := want.Text[field]; ok && to != text[field] {
			out = append(out, FieldChange{Field: field, From: text[field], To: to})
		}
	}
	if want.Categories != nil && !sameSet(*want.Categories, now.Categories) {
		out = append(out, FieldChange{Field: "categories", From: fieldValue(now, "categories", catNames),
			To: joinNames(*want.Categories, catNames)})
	}
	return out
}

func fieldValue(r paprika.Recipe, field string, catNames map[string]string) string {
	switch field {
	case "rating":
		return fmt.Sprint(r.Rating)
	case "categories":
		return joinNames(r.Categories, catNames)
	default:
		return recipeText(r)[field]
	}
}

// recipeTextInput cleans and checks the text fields a tool was given; nil ones
// are left out. A field that is only whitespace counts as empty, and one in
// required must not be empty.
func recipeTextInput(given map[string]*string, required map[string]bool) (map[string]string, *ToolError) {
	out := map[string]string{}
	for _, field := range paprika.RecipeTextFields {
		v := cleanText(given[field])
		if v == nil {
			continue
		}
		if singleLine[field] {
			*v = strings.TrimSpace(*v)
			if strings.ContainsAny(*v, "\r\n") {
				return nil, invalid(field + " must be a single line")
			}
		}
		if strings.TrimSpace(*v) == "" {
			*v = ""
		}
		if terr := checkRecipeText(field, *v); terr != nil {
			return nil, terr
		}
		if required[field] && *v == "" {
			return nil, invalid(field + " cannot be empty")
		}
		out[field] = *v
	}
	return out, nil
}

func checkRecipeText(field, v string) *ToolError {
	limit := maxRecipeText
	if singleLine[field] {
		limit = maxRecipeLine
	}
	if utf8.RuneCountInString(v) > limit {
		return invalid(fmt.Sprintf("%s is longer than %d characters", field, limit))
	}
	if strings.ContainsRune(v, 0) || !utf8.ValidString(v) {
		return invalid(field + " is not valid text")
	}
	return nil
}

// recipeText gives the editable text fields of a recipe by their Paprika names.
func recipeText(r paprika.Recipe) map[string]string {
	return map[string]string{
		"name": r.Name, "ingredients": r.Ingredients, "directions": r.Directions, "description": r.Description, "notes": r.Notes,
		"nutritional_info": r.NutritionalInfo, "servings": r.Servings, "prep_time": r.PrepTime, "cook_time": r.CookTime,
		"total_time": r.TotalTime, "difficulty": r.Difficulty, "source": r.Source, "source_url": r.SourceURL,
	}
}

// categoryIndex resolves category names for an edit and names uids for its summary.
type categoryIndex struct {
	names  map[string]string   // uid -> name
	byName map[string][]string // normalised name -> uids
}

func (s *server) categoryIndex(ctx context.Context) (categoryIndex, *ToolError) {
	cats, err := store.Load[paprika.Category](ctx, s.Store, paprika.Categories)
	if err != nil {
		return categoryIndex{}, fromErr(err)
	}
	idx := categoryIndex{names: map[string]string{}, byName: map[string][]string{}}
	for _, c := range cats {
		idx.names[c.UID] = c.Name
		idx.byName[norm(c.Name)] = append(idx.byName[norm(c.Name)], c.UID)
	}
	return idx, nil
}

// resolve turns category names into uids. An unknown or ambiguous name is refused:
// the adapter does not create categories.
func (i categoryIndex) resolve(names []string) ([]string, *ToolError) {
	uids := []string{}
	for _, n := range names {
		found := i.byName[norm(n)]
		switch {
		case strings.TrimSpace(n) == "":
			return nil, invalid("a category name is empty")
		case len(found) == 0:
			known := slices.Sorted(maps.Values(i.names))
			return nil, notFound(fmt.Sprintf("no category named %q; Paprika has: %s", n, strings.Join(known, ", ")))
		case len(found) > 1:
			return nil, invalid(fmt.Sprintf("%d categories are named %q; rename one in Paprika first", len(found), n))
		}
		if !slices.Contains(uids, found[0]) {
			uids = append(uids, found[0])
		}
	}
	return uids, nil
}

func joinNames(uids []string, names map[string]string) string {
	out := make([]string, 0, len(uids))
	for _, u := range uids {
		if n, ok := names[u]; ok {
			out = append(out, n)
		} else {
			out = append(out, u)
		}
	}
	slices.Sort(out)
	return strings.Join(out, ", ")
}

func sameSet(a, b []string) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(slices.Compact(a), slices.Compact(b))
}

func orNone(v string) string {
	if v == "" {
		return "(none)"
	}
	return v
}

func cleanText(v *string) *string {
	if v == nil {
		return nil
	}
	s := strings.ReplaceAll(*v, "\r\n", "\n")
	return &s
}

func stars(n string) string {
	if n == "0" {
		return "unrated"
	}
	return n + " of 5"
}

// preview shortens text for the one-line summary; the full text is in the proposal's changes.
func preview(text string) string {
	line := strings.Join(strings.Fields(text), " ")
	if r := []rune(line); len(r) > 160 {
		return fmt.Sprintf("%q... (%d characters in all)", string(r[:160]), utf8.RuneCountInString(text))
	}
	return fmt.Sprintf("%q", line)
}
