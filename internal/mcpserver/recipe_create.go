package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/pavel-alay/paprika-adapter/internal/paprika"
)

type ProposeRecipeCreateIn struct {
	Name            string   `json:"name" jsonschema:"the recipe's name"`
	Ingredients     string   `json:"ingredients,omitempty" jsonschema:"one ingredient per line, as Paprika stores it (e.g. 200 g flour); a line ending in a colon is a heading"`
	Directions      string   `json:"directions,omitempty" jsonschema:"one step per line"`
	Description     string   `json:"description,omitempty"`
	Notes           string   `json:"notes,omitempty"`
	NutritionalInfo string   `json:"nutritional_info,omitempty" jsonschema:"nutrition as free text"`
	Servings        string   `json:"servings,omitempty" jsonschema:"servings as free text, e.g. 4"`
	PrepTime        string   `json:"prep_time,omitempty" jsonschema:"preparation time in minutes, e.g. 15 min, which the apps can parse back"`
	CookTime        string   `json:"cook_time,omitempty" jsonschema:"cooking time in minutes, e.g. 80 min"`
	TotalTime       string   `json:"total_time,omitempty" jsonschema:"total time in minutes, e.g. 95 min"`
	Difficulty      string   `json:"difficulty,omitempty" jsonschema:"difficulty as free text, e.g. Easy"`
	Source          string   `json:"source,omitempty" jsonschema:"where the recipe comes from, e.g. a book or site name"`
	SourceURL       string   `json:"source_url,omitempty" jsonschema:"link to the original recipe"`
	Categories      []string `json:"categories,omitempty" jsonschema:"categories by name; they must exist in Paprika"`
	Rating          int      `json:"rating,omitempty" jsonschema:"stars, 1 to 5; default unrated"`
	AllowDuplicate  bool     `json:"allow_duplicate,omitempty" jsonschema:"create it even if a recipe with the same name exists outside the trash; default false"`
}

func (s *server) proposeRecipeCreate(ctx context.Context, in ProposeRecipeCreateIn, out *ProposeOut) *ToolError {
	given := map[string]*string{"name": &in.Name, "ingredients": &in.Ingredients, "directions": &in.Directions, "description": &in.Description,
		"notes": &in.Notes, "nutritional_info": &in.NutritionalInfo, "servings": &in.Servings, "prep_time": &in.PrepTime, "cook_time": &in.CookTime,
		"total_time": &in.TotalTime, "difficulty": &in.Difficulty, "source": &in.Source, "source_url": &in.SourceURL}
	text, terr := recipeTextInput(given, map[string]bool{"name": true})
	if terr != nil {
		return terr
	}
	for f, v := range text {
		if v == "" {
			delete(text, f)
		}
	}
	if in.Rating < 0 || in.Rating > 5 {
		return invalid("rating must be 0 to 5")
	}
	fresh := []string{paprika.Recipes}
	if len(in.Categories) > 0 {
		fresh = append(fresh, string(paprika.Categories))
	}
	if terr := s.ensureFresh(ctx, &out.Meta, fresh...); terr != nil {
		return terr
	}
	cats, terr := s.categoryIndex(ctx)
	if terr != nil {
		return terr
	}
	catUIDs, terr := cats.resolve(in.Categories)
	if terr != nil {
		return terr
	}
	if dup, terr := s.recipeNamed(ctx, text["name"], ""); terr != nil {
		return terr
	} else if dup != nil && !in.AllowDuplicate {
		return &ToolError{Code: "duplicate", Message: fmt.Sprintf("a recipe named %q already exists (uid %s); pass allow_duplicate=true to create another", dup.Name, dup.UID)}
	}
	now := s.Now().In(s.Location)
	recipe := paprika.NewRecipe{UID: paprika.NewUID(), Text: text, Rating: in.Rating, Categories: catUIDs, Created: now.Format(paprika.TimeLayout)}
	view := newRecipeView(recipe, cats.names)
	var parts []string
	for _, c := range view.Changes {
		if c.Field == "name" {
			continue
		}
		label := strings.ReplaceAll(c.Field, "_", " ")
		switch {
		case c.Field == "rating":
			parts = append(parts, "rating "+stars(c.To))
		case singleLine[c.Field] || c.Field == "categories":
			parts = append(parts, fmt.Sprintf("%s %s", label, c.To))
		default:
			parts = append(parts, fmt.Sprintf("%s (%d lines): %s", label, strings.Count(c.To, "\n")+1, preview(c.To)))
		}
	}
	summary := fmt.Sprintf("Create the Paprika recipe %q.", text["name"])
	if len(parts) > 0 {
		summary = fmt.Sprintf("Create the Paprika recipe %q with %s.", text["name"], strings.Join(parts, "; "))
	}
	p := payload{RecipeCreate: &recipe, AllowDuplicates: in.AllowDuplicate, Summary: summary}
	pv, terr := s.saveProposal(ctx, kindRecipeCreate, p)
	if terr != nil {
		return terr
	}
	pv.RecipeNew = &view
	out.Proposal = pv
	return nil
}

// commitRecipeCreate posts the recipe unless Paprika already holds its uid, then
// reads it back and puts it in the cache.
func (s *server) commitRecipeCreate(ctx context.Context, p payload, out *CommitOut) *ToolError {
	want := *p.RecipeCreate
	cats, terr := s.categoryIndex(ctx)
	if terr != nil {
		return terr
	}
	stored, err := s.Client.Recipe(ctx, want.UID)
	var perr *paprika.Error
	switch {
	case err == nil: // an earlier attempt landed, or the uid is taken
	case errors.As(err, &perr) && perr.Kind == paprika.KindNotFound:
		if dup, terr := s.recipeNamed(ctx, want.Text["name"], ""); terr != nil {
			return terr
		} else if dup != nil && !p.AllowDuplicates {
			return &ToolError{Code: "duplicate", Message: "nothing was written: a recipe with that name was created in the meantime (uid " + dup.UID + ")"}
		}
		if _, err := s.Client.CreateRecipe(ctx, want); err != nil {
			return writeFailed(err)
		}
		if stored, err = s.Client.Recipe(ctx, want.UID); err != nil {
			return notVerified(fromErr(err).Message)
		}
	default:
		return fromErr(err)
	}
	got, err := paprika.Decode[paprika.Recipe](stored.JSON)
	if err != nil {
		return fromErr(err)
	}
	if field := unlike(want, got); field != "" {
		if got.Created != want.Created {
			return &ToolError{Code: "uid_conflict", Message: "nothing was overwritten: Paprika already holds a different recipe under uid " + want.UID + "; propose again"}
		}
		return notVerified("Paprika accepted the recipe but returns another " + field)
	}
	if err := s.Store.PutRecipe(ctx, stored, got.Hash, s.Now()); err != nil {
		return fromErr(err)
	}
	view := newRecipeView(paprika.NewRecipe{UID: got.UID, Text: recipeText(got), Rating: got.Rating, Categories: got.Categories}, cats.names)
	out.RecipeNew = &view
	out.Sync = s.Sync.Freshness(ctx, paprika.Recipes)
	return nil
}

// unlike returns the first field the new recipe was given that got does not
// hold as given, or "" when got is the recipe as created.
func unlike(want paprika.NewRecipe, got paprika.Recipe) string {
	text := recipeText(got)
	for _, f := range paprika.RecipeTextFields {
		if text[f] != want.Text[f] {
			return strings.ReplaceAll(f, "_", " ")
		}
	}
	if got.Rating != want.Rating {
		return "rating"
	}
	if !sameSet(got.Categories, want.Categories) {
		return "categories"
	}
	return ""
}

// newRecipeView lists every field a new recipe is given, in the order edits list them.
func newRecipeView(r paprika.NewRecipe, catNames map[string]string) RecipeEditView {
	v := RecipeEditView{Recipe: NamedRef{UID: r.UID, Name: r.Text["name"]}}
	if r.Rating != 0 {
		v.Changes = append(v.Changes, FieldChange{Field: "rating", From: "0", To: fmt.Sprint(r.Rating)})
	}
	for _, f := range paprika.RecipeTextFields {
		if t := r.Text[f]; t != "" {
			v.Changes = append(v.Changes, FieldChange{Field: f, To: t})
		}
	}
	if len(r.Categories) > 0 {
		v.Changes = append(v.Changes, FieldChange{Field: "categories", To: joinNames(r.Categories, catNames)})
	}
	return v
}

// recipeNamed returns a cached recipe outside the trash with the same name, if
// any, other than the recipe with uid except.
func (s *server) recipeNamed(ctx context.Context, name, except string) (*NamedRef, *ToolError) {
	names, err := s.Store.ActiveRecipeNames(ctx)
	if err != nil {
		return nil, fromErr(err)
	}
	for uid, n := range names {
		if uid != except && norm(n) == norm(name) {
			return &NamedRef{UID: uid, Name: n}, nil
		}
	}
	return nil, nil
}

// renameClash refuses an edit that renames a recipe to the name of another
// recipe outside the trash, unless allowed. now is the recipe before the edit.
func (s *server) renameClash(ctx context.Context, edit recipeEdit, now paprika.Recipe, allow bool, msg string) *ToolError {
	name, ok := edit.Edit.Text["name"]
	if !ok || allow || norm(name) == norm(now.Name) {
		return nil
	}
	dup, terr := s.recipeNamed(ctx, name, now.UID)
	if terr != nil || dup == nil {
		return terr
	}
	return &ToolError{Code: "duplicate", Message: fmt.Sprintf(msg, dup.Name, dup.UID)}
}
