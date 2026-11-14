package mcpserver

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/pavel-alay/paprika-adapter/internal/paprika"
	"github.com/pavel-alay/paprika-adapter/internal/store"
)

type ProposePantryEditIn struct {
	ItemUID         string  `json:"item_uid" jsonschema:"uid of the pantry item to change, from list_pantry"`
	Ingredient      string  `json:"ingredient,omitempty" jsonschema:"rename the item; its aisle stays as it is"`
	Quantity        *string `json:"quantity,omitempty" jsonschema:"new amount as free text, e.g. 500 g; an empty string clears it"`
	InStock         *bool   `json:"in_stock,omitempty" jsonschema:"false marks the item as used up, true as back in stock"`
	ExpirationDate  string  `json:"expiration_date,omitempty" jsonschema:"set the expiration date, YYYY-MM-DD; not together with clear_expiration"`
	ClearExpiration bool    `json:"clear_expiration,omitempty" jsonschema:"remove the expiration date"`
	PurchaseDate    string  `json:"purchase_date,omitempty" jsonschema:"set the purchase date, YYYY-MM-DD"`
	Location        string  `json:"location,omitempty" jsonschema:"move the item to this pantry location, by name, e.g. Freezer; it must already exist in Paprika"`
	Notes           *string `json:"notes,omitempty" jsonschema:"new free-text note; an empty string removes it"`
	AllowDuplicate  bool    `json:"allow_duplicate,omitempty" jsonschema:"allow renaming to an ingredient another pantry item already has; default false"`
}

// pantryEdit is what a proposal stores.
type pantryEdit struct {
	UID    string             `json:"uid"`
	Edit   paprika.PantryEdit `json:"edit"`
	Before PantryEntry        `json:"before"` // as shown to the user when proposing
}

func (s *server) proposePantryEdit(ctx context.Context, in ProposePantryEditIn, out *ProposeOut) *ToolError {
	uid, name := strings.TrimSpace(in.ItemUID), strings.TrimSpace(in.Ingredient)
	expires, bought := strings.TrimSpace(in.ExpirationDate), strings.TrimSpace(in.PurchaseDate)
	if uid == "" {
		return invalid("item_uid is required; get it from list_pantry")
	}
	if expires != "" && in.ClearExpiration {
		return invalid("give expiration_date or clear_expiration, not both")
	}
	edit := pantryEdit{UID: uid, Edit: paprika.PantryEdit{InStock: in.InStock}}
	var parts []string
	if name != "" {
		if terr := checkText("ingredient", name); terr != nil {
			return terr
		}
		edit.Edit.Ingredient = &name
		parts = append(parts, fmt.Sprintf("rename it to %q", name))
	}
	if in.Quantity != nil {
		qty := strings.TrimSpace(*in.Quantity)
		if terr := checkText("quantity", qty); terr != nil {
			return terr
		}
		edit.Edit.Quantity = &qty
		parts = append(parts, fmt.Sprintf("set the quantity to %q", qty))
		if qty == "" {
			parts[len(parts)-1] = "clear the quantity"
		}
	}
	if in.InStock != nil {
		parts = append(parts, map[bool]string{true: "mark it in stock", false: "mark it out of stock"}[*in.InStock])
	}
	for field, day := range map[string]string{"expiration_date": expires, "purchase_date": bought} {
		if _, err := time.Parse(dayLayout, day); day != "" && err != nil {
			return invalid(field + " must look like 2027-03-01")
		}
	}
	switch {
	case in.ClearExpiration:
		edit.Edit.Expiration = new(string)
		parts = append(parts, "remove the expiration date")
	case expires != "":
		stamp := expires + " 00:00:00"
		edit.Edit.Expiration = &stamp
		parts = append(parts, "set the expiration date to "+expires)
	}
	if bought != "" {
		stamp := bought + " 00:00:00"
		edit.Edit.PurchaseDate = &stamp
		parts = append(parts, "set the purchase date to "+bought)
	}
	if in.Notes != nil {
		notes := strings.TrimSpace(*in.Notes)
		if terr := checkText("notes", notes); terr != nil {
			return terr
		}
		edit.Edit.Notes = &notes
		parts = append(parts, fmt.Sprintf("set the notes to %q", notes))
		if notes == "" {
			parts[len(parts)-1] = "remove the notes"
		}
	}
	if len(parts) == 0 && strings.TrimSpace(in.Location) == "" {
		return invalid("give at least one of ingredient, quantity, in_stock, expiration_date, clear_expiration, purchase_date, location, notes")
	}
	if terr := s.ensureFresh(ctx, &out.Meta, string(paprika.Pantry), string(paprika.PantryLocations)); terr != nil {
		return terr
	}
	places, err := s.pantryPlaces(ctx)
	if err != nil {
		return fromErr(err)
	}
	if strings.TrimSpace(in.Location) != "" {
		l, terr := places.resolve("location", in.Location)
		if terr != nil {
			return terr
		}
		edit.Edit.LocationUID = &l.UID
		parts = append(parts, "move it to "+l.Name)
	}
	existing, err := store.Load[paprika.PantryItem](ctx, s.Store, paprika.Pantry)
	if err != nil {
		return fromErr(err)
	}
	current, ok := findPantry(existing, uid)
	if !ok {
		return notFound("no pantry item with uid " + uid + "; see list_pantry")
	}
	after := applyPantryEdit(current, edit.Edit)
	before, result := pantryEntry(current), pantryEntry(after)
	if before.LocationUID != result.LocationUID && places.name(before.LocationUID) == places.name(result.LocationUID) {
		result.LocationUID = before.LocationUID // already there: the default location, stored as no uid
		edit.Edit.LocationUID = nil
		parts = parts[:len(parts)-1]
	}
	if result == before || len(parts) == 0 {
		return &ToolError{Code: "duplicate", Message: "nothing to change: the item already is as requested"}
	}
	if dup := renamedOnto(existing, current, after); dup != nil && !in.AllowDuplicate {
		return &ToolError{Code: "duplicate", Message: fmt.Sprintf("the pantry already holds %q (item uid %s); pass allow_duplicate=true to have it twice",
			dup.Ingredient, dup.UID)}
	}
	edit.Before = before
	p := payload{PantryEdit: &edit, AllowDuplicates: in.AllowDuplicate,
		Summary: fmt.Sprintf("Change the Paprika pantry item %q: %s. This overwrites the item.", before.Ingredient, strings.Join(parts, "; "))}
	view, terr := s.saveProposal(ctx, kindPantryEdit, p)
	if terr != nil {
		return terr
	}
	view.Pantry, view.PantryBefore = []PantryEntry{result}, []PantryEntry{before}
	places.label(view.Pantry)
	places.label(view.PantryBefore)
	out.Proposal = view
	return nil
}

// commitPantryEdit reads the pantry from Paprika, overwrites the edited fields
// of the item on that copy and reads it back.
func (s *server) commitPantryEdit(ctx context.Context, p payload, out *CommitOut) *ToolError {
	edit := *p.PantryEdit
	raws, items, terr := pullRows[paprika.PantryItem](ctx, s, paprika.Pantry)
	if terr != nil {
		return terr
	}
	current, ok := findPantry(items, edit.UID)
	if !ok {
		return notFound("the pantry item is no longer in Paprika; it was removed in the meantime")
	}
	want := applyPantryEdit(current, edit.Edit)
	if pantryEntry(current) != pantryEntry(want) { // otherwise an earlier attempt already landed
		if dup := renamedOnto(items, current, want); dup != nil && !p.AllowDuplicates {
			return &ToolError{Code: "duplicate", Message: "nothing was written: an item with that ingredient was added to the pantry in the meantime (item uid " + dup.UID + ")"}
		}
		if err := s.Client.EditPantryItem(ctx, raws[edit.UID], edit.Edit); err != nil {
			return writeFailed(err)
		}
		if _, items, terr = pullRows[paprika.PantryItem](ctx, s, paprika.Pantry); terr != nil {
			return notVerified(terr.Message)
		}
	}
	got, ok := findPantry(items, edit.UID)
	if !ok || pantryEntry(got) != pantryEntry(want) {
		return notVerified("Paprika accepted the item but still returns it unchanged")
	}
	out.Pantry, out.PantryBefore = []PantryEntry{pantryEntry(got)}, []PantryEntry{edit.Before}
	s.labelPantry(ctx, out.Pantry, out.PantryBefore)
	out.Sync = s.Sync.Freshness(ctx, string(paprika.Pantry))
	return nil
}

func applyPantryEdit(it paprika.PantryItem, e paprika.PantryEdit) paprika.PantryItem {
	if e.Ingredient != nil {
		it.Ingredient = *e.Ingredient
	}
	if e.Quantity != nil {
		it.Quantity = *e.Quantity
	}
	if e.InStock != nil {
		it.InStock = *e.InStock
	}
	if e.Expiration != nil {
		it.HasExpiration, it.ExpirationDate = *e.Expiration != "", e.Expiration
	}
	if e.PurchaseDate != nil {
		it.PurchaseDate = e.PurchaseDate
	}
	if e.LocationUID != nil {
		it.LocationUID = e.LocationUID
	}
	if e.Notes != nil {
		it.Notes = nil
		if *e.Notes != "" {
			it.Notes = e.Notes
		}
	}
	return it
}

// renamedOnto returns another pantry item that already has the ingredient an edit renames to.
func renamedOnto(existing []paprika.PantryItem, before, after paprika.PantryItem) *paprika.PantryItem {
	if norm(before.Ingredient) == norm(after.Ingredient) {
		return nil
	}
	return duplicatePantry(existing, paprika.NewPantryItem{UID: after.UID, Ingredient: after.Ingredient})
}
