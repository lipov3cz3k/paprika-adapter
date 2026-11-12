package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/pavel-alay/paprika-adapter/internal/paprika"
	"github.com/pavel-alay/paprika-adapter/internal/store"
)

type ProposePantryRemovalIn struct {
	ItemUIDs []string `json:"item_uids" jsonschema:"uids of the 1 to 25 pantry items to remove, from list_pantry"`
}

func (s *server) proposePantryRemoval(ctx context.Context, in ProposePantryRemovalIn, out *ProposeOut) *ToolError {
	uids, terr := uidList("item_uids", in.ItemUIDs, maxPantryItems, "list_pantry")
	if terr != nil {
		return terr
	}
	if terr := s.ensureFresh(ctx, &out.Meta, string(paprika.Pantry)); terr != nil {
		return terr
	}
	existing, err := store.Load[paprika.PantryItem](ctx, s.Store, paprika.Pantry)
	if err != nil {
		return fromErr(err)
	}
	var p payload
	var lines []string
	for _, uid := range uids {
		item, ok := findPantry(existing, uid)
		if !ok {
			return notFound("no pantry item with uid " + uid + "; see list_pantry")
		}
		p.PantryRemoval = append(p.PantryRemoval, pantryEntry(item))
		lines = append(lines, strings.TrimSpace(item.Quantity+" "+item.Ingredient))
	}
	p.Summary = fmt.Sprintf("REMOVE %d item(s) from the Paprika pantry: %s. They disappear from the pantry on every device.",
		len(p.PantryRemoval), strings.Join(lines, "; "))
	view, terr := s.saveProposal(ctx, kindPantryRemoval, p)
	if terr != nil {
		return terr
	}
	view.PantryRemoved = p.PantryRemoval
	out.Proposal = view
	return nil
}

// commitPantryRemoval removes the proposed items that Paprika still holds and
// checks that none of them is returned any more.
func (s *server) commitPantryRemoval(ctx context.Context, p payload, out *CommitOut) *ToolError {
	rows := make([]removal, 0, len(p.PantryRemoval))
	for _, it := range p.PantryRemoval {
		rows = append(rows, removal{it.UID, it.Ingredient})
	}
	if _, terr := removeRows(ctx, s, paprika.Pantry, rows, nil, s.Client.RemovePantryItems); terr != nil {
		return terr
	}
	out.PantryRemoved = p.PantryRemoval
	out.Sync = s.Sync.Freshness(ctx, string(paprika.Pantry))
	return nil
}
