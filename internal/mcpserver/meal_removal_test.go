package mcpserver_test

import "testing"

func TestMealRemovalProposeCommitReadBack(t *testing.T) {
	h := newHarness(t, true)
	p := h.ok("propose_meal_plan_removal", map[string]any{"meal_uids": []any{soupMeal, soupMeal}}).Proposal
	if p.Kind != "meal_plan_removal" || len(p.MealsRemoved) != 1 || p.MealsRemoved[0].Name != "Red Lentil Soup" {
		t.Fatalf("proposal = %+v", p)
	}
	want := `REMOVE 1 entry from the Paprika meal plan: "Red Lentil Soup" as Dinner on 2026-10-05 (Monday). They disappear from the plan on every device.`
	if p.Summary != want {
		t.Fatalf("summary = %s", p.Summary)
	}
	if h.posts("meals") != 0 {
		t.Fatal("proposing wrote to Paprika")
	}
	h.fake.DropNextWriteAck()
	h.fails("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true}, "upstream_unavailable")
	c := h.ok("commit_proposal", map[string]any{"proposal_id": p.ProposalID, "confirm": true})
	if c.Status != "committed" || !c.Verified || len(c.MealsRemoved) != 1 || h.posts("meals") != 1 || mealRow(h, soupMeal) != nil ||
		len(h.fake.Rows("meals")) != 4 {
		t.Fatalf("commit = %+v posts=%d", c, h.posts("meals"))
	}
	for _, m := range h.ok("get_meal_plan", nil).Meals {
		if m.UID == soupMeal {
			t.Fatal("get_meal_plan still shows the removed entry")
		}
	}
	h.fails("propose_meal_plan_removal", map[string]any{"meal_uids": []any{soupMeal}}, "not_found")
}

func TestMealRemovalInputsMustBeExplicit(t *testing.T) {
	h := newHarness(t, true)
	for name, args := range map[string]map[string]any{
		"no entries": {"meal_uids": []any{}},
		"blank uid":  {"meal_uids": []any{soupMeal, ""}},
	} {
		t.Run(name, func(t *testing.T) {
			h.fails("propose_meal_plan_removal", args, "invalid_input")
		})
	}
	h.fails("propose_meal_plan_removal", map[string]any{"meal_uids": []any{soupMeal, "NOPE"}}, "not_found")
	if h.posts("meals") != 0 || len(h.fake.Rows("meals")) != 5 {
		t.Fatal("a refused removal reached Paprika")
	}
}
