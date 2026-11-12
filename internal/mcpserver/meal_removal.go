package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/pavel-alay/paprika-adapter/internal/paprika"
	"github.com/pavel-alay/paprika-adapter/internal/store"
)

type ProposeMealRemovalIn struct {
	MealUIDs []string `json:"meal_uids" jsonschema:"uids of the 1 to 25 meal-plan entries to remove, from get_meal_plan"`
}

const maxMealRemovals = 25

func (s *server) proposeMealRemoval(ctx context.Context, in ProposeMealRemovalIn, out *ProposeOut) *ToolError {
	uids, terr := uidList("meal_uids", in.MealUIDs, maxMealRemovals, "get_meal_plan")
	if terr != nil {
		return terr
	}
	if terr := s.ensureFresh(ctx, &out.Meta, string(paprika.Meals), string(paprika.MealTypes)); terr != nil {
		return terr
	}
	existing, err := store.Load[paprika.Meal](ctx, s.Store, paprika.Meals)
	if err != nil {
		return fromErr(err)
	}
	types, err := s.mealTypes(ctx)
	if err != nil {
		return fromErr(err)
	}
	var p payload
	var lines []string
	for _, uid := range uids {
		m, ok := findMeal(existing, uid)
		if !ok {
			return notFound("no meal-plan entry with uid " + uid + "; see get_meal_plan")
		}
		e := types.entry(m)
		p.MealRemoval = append(p.MealRemoval, e)
		lines = append(lines, fmt.Sprintf("%q as %s on %s", e.Name, e.MealType, dayWithWeekday(e.Date)))
	}
	p.Summary = fmt.Sprintf("REMOVE %d entr%s from the Paprika meal plan: %s. They disappear from the plan on every device.",
		len(p.MealRemoval), map[bool]string{true: "y", false: "ies"}[len(p.MealRemoval) == 1], strings.Join(lines, "; "))
	view, terr := s.saveProposal(ctx, kindMealRemoval, p)
	if terr != nil {
		return terr
	}
	view.MealsRemoved = p.MealRemoval
	out.Proposal = view
	return nil
}

func (s *server) commitMealRemoval(ctx context.Context, p payload, out *CommitOut) *ToolError {
	rows := make([]removal, 0, len(p.MealRemoval))
	for _, m := range p.MealRemoval {
		rows = append(rows, removal{m.UID, m.Name})
	}
	if _, terr := removeRows(ctx, s, paprika.Meals, rows, nil, s.Client.RemoveMeals); terr != nil {
		return terr
	}
	out.MealsRemoved = p.MealRemoval
	out.Sync = s.Sync.Freshness(ctx, string(paprika.Meals))
	return nil
}
