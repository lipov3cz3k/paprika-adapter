package mcpserver

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/pavel-alay/paprika-adapter/internal/paprika"
)

type ProposeRecipeTrashIn struct {
	UIDs    []string `json:"uids" jsonschema:"uids of the 1 to 25 recipes to move to Paprika's trash, from search_recipes"`
	Restore bool     `json:"restore,omitempty" jsonschema:"true takes the recipes out of the trash instead; find them with search_recipes include_trashed=true"`
}

const maxRecipeTrash = 25

// recipeTrash is what a proposal stores.
type recipeTrash struct {
	InTrash bool       `json:"in_trash"`
	Recipes []NamedRef `json:"recipes"`
}

func (s *server) proposeRecipeTrash(ctx context.Context, in ProposeRecipeTrashIn, out *ProposeOut) *ToolError {
	uids, terr := recipeUIDs(in.UIDs)
	if terr != nil {
		return terr
	}
	if terr := s.ensureFresh(ctx, &out.Meta, paprika.Recipes); terr != nil {
		return terr
	}
	trash := recipeTrash{InTrash: !in.Restore}
	var skipped []SkippedItem
	var names []string
	for _, uid := range uids {
		cached, ok, err := s.Store.Recipe(ctx, uid)
		if err != nil {
			return fromErr(err)
		}
		if !ok {
			return notFound("no recipe with uid " + uid)
		}
		r, err := paprika.Decode[paprika.Recipe](cached.Raw)
		if err != nil {
			return fromErr(err)
		}
		if r.InTrash == trash.InTrash {
			skipped = append(skipped, SkippedItem{Name: r.Name, ExistingUID: r.UID,
				Reason: map[bool]string{true: "already in the trash", false: "not in the trash"}[trash.InTrash]})
			continue
		}
		trash.Recipes = append(trash.Recipes, NamedRef{UID: r.UID, Name: r.Name})
		names = append(names, fmt.Sprintf("%q", r.Name))
	}
	if len(trash.Recipes) == 0 {
		return &ToolError{Code: "duplicate", Message: "nothing to change: every recipe already is as requested"}
	}
	summary := fmt.Sprintf("Move %d recipe(s) to the Paprika trash: %s. They can be taken out of the trash again until it is emptied in the app.",
		len(names), strings.Join(names, "; "))
	if in.Restore {
		summary = fmt.Sprintf("Take %d recipe(s) out of the Paprika trash: %s.", len(names), strings.Join(names, "; "))
	}
	p := payload{RecipeTrash: &trash, Skipped: skipped, Summary: summary}
	view, terr := s.saveProposal(ctx, kindRecipeTrash, p)
	if terr != nil {
		return terr
	}
	view.RecipesTrashed, view.Skipped = trash.Recipes, skipped
	out.Proposal = view
	return nil
}

// commitRecipeTrash sets in_trash on each proposed recipe that does not have it
// yet, reading each one from Paprika first and back afterwards.
func (s *server) commitRecipeTrash(ctx context.Context, p payload, out *CommitOut) *ToolError {
	trash := *p.RecipeTrash
	for _, ref := range trash.Recipes {
		current, err := s.Client.Recipe(ctx, ref.UID)
		if err != nil {
			return fromErr(err)
		}
		r, err := paprika.Decode[paprika.Recipe](current.JSON)
		if err != nil {
			return fromErr(err)
		}
		stored := current
		if r.InTrash != trash.InTrash { // otherwise an earlier attempt, or someone in the app, already did it
			if _, err := s.Client.WriteRecipe(ctx, current, paprika.RecipeEdit{InTrash: &trash.InTrash}); err != nil {
				return writeFailed(err)
			}
			if stored, err = s.Client.Recipe(ctx, ref.UID); err != nil {
				return notVerified(fromErr(err).Message)
			}
			if r, err = paprika.Decode[paprika.Recipe](stored.JSON); err != nil {
				return fromErr(err)
			}
			if r.InTrash != trash.InTrash {
				return notVerified("Paprika accepted " + r.Name + " but still returns it " + map[bool]string{true: "outside", false: "inside"}[trash.InTrash] + " the trash")
			}
		}
		if err := s.Store.PutRecipe(ctx, stored, r.Hash, s.Now()); err != nil {
			return fromErr(err)
		}
	}
	out.RecipesTrashed, out.Skipped = trash.Recipes, p.Skipped
	out.Sync = s.Sync.Freshness(ctx, paprika.Recipes)
	return nil
}

// recipeUIDs trims and de-duplicates the uids a trash proposal names.
func recipeUIDs(in []string) ([]string, *ToolError) {
	if len(in) == 0 || len(in) > maxRecipeTrash {
		return nil, invalid(fmt.Sprintf("uids must hold 1 to %d entries; get them from search_recipes", maxRecipeTrash))
	}
	var out []string
	for i, raw := range in {
		uid := strings.TrimSpace(raw)
		if uid == "" {
			return nil, invalid(fmt.Sprintf("uids[%d] is empty", i))
		}
		if !slices.Contains(out, uid) {
			out = append(out, uid)
		}
	}
	return out, nil
}
