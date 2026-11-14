package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/pavel-alay/paprika-adapter/internal/ingredients"
	"github.com/pavel-alay/paprika-adapter/internal/paprika"
	"github.com/pavel-alay/paprika-adapter/internal/store"
)

type ProposeGroceryEditIn struct {
	ItemUID        string  `json:"item_uid" jsonschema:"uid of the grocery item to change, from list_grocery_items"`
	Name           string  `json:"name,omitempty" jsonschema:"what to buy instead, without the amount, e.g. oat milk"`
	Quantity       *string `json:"quantity,omitempty" jsonschema:"new amount as free text, e.g. 2 l; an empty string clears it"`
	Instruction    *string `json:"instruction,omitempty" jsonschema:"new note shown with the item; an empty string clears it"`
	Aisle          string  `json:"aisle,omitempty" jsonschema:"move the item to this aisle, by name, e.g. Dairy; it must already exist in Paprika"`
	ListUID        string  `json:"list_uid,omitempty" jsonschema:"move the item to this grocery list, from list_grocery_lists"`
	Purchased      *bool   `json:"purchased,omitempty" jsonschema:"true ticks the item off as bought, false puts it back on the list"`
	AllowDuplicate bool    `json:"allow_duplicate,omitempty" jsonschema:"allow the result to repeat an unpurchased item already on that list; default false"`
}

// groceryEdit is what a proposal stores.
type groceryEdit struct {
	UID    string              `json:"uid"`
	Edit   paprika.GroceryEdit `json:"edit"`
	Before GroceryEntry        `json:"before"` // as shown to the user when proposing
}

func (s *server) proposeGroceryEdit(ctx context.Context, in ProposeGroceryEditIn, out *ProposeOut) *ToolError {
	uid, name := strings.TrimSpace(in.ItemUID), strings.TrimSpace(in.Name)
	aisleName, listUID := strings.TrimSpace(in.Aisle), strings.TrimSpace(in.ListUID)
	if uid == "" {
		return invalid("item_uid is required; get it from list_grocery_items")
	}
	if name == "" && in.Quantity == nil && in.Instruction == nil && aisleName == "" && listUID == "" && in.Purchased == nil {
		return invalid("give at least one of name, quantity, instruction, aisle, list_uid, purchased")
	}
	for _, f := range []struct {
		field string
		v     *string
	}{{"name", &name}, {"quantity", in.Quantity}, {"instruction", in.Instruction}} { // in order, so the first bad field is the one reported
		if f.v == nil {
			continue
		}
		*f.v = strings.TrimSpace(*f.v)
		if terr := checkText(f.field, *f.v); terr != nil {
			return terr
		}
	}
	cols := []string{string(paprika.GroceryLists), string(paprika.Groceries)}
	if aisleName != "" {
		cols = append(cols, string(paprika.GroceryAisles))
	}
	if terr := s.ensureFresh(ctx, &out.Meta, cols...); terr != nil {
		return terr
	}
	existing, err := store.Load[paprika.GroceryItem](ctx, s.Store, paprika.Groceries)
	if err != nil {
		return fromErr(err)
	}
	current, ok := findGrocery(existing, uid)
	if !ok {
		return notFound("no grocery item with uid " + uid + "; see list_grocery_items")
	}
	edit := groceryEdit{UID: uid, Edit: paprika.GroceryEdit{Purchased: in.Purchased, Instruction: in.Instruction}}
	if name != "" || in.Quantity != nil {
		qty, ingredient := groceryLine(current) // a new name keeps the amount, even one written only in the line
		if name != "" {
			ingredient = name
			edit.Edit.Ingredient = &name
		}
		if in.Quantity != nil {
			qty = *in.Quantity
			edit.Edit.Quantity = &qty
		}
		line := strings.TrimSpace(qty + " " + ingredient)
		edit.Edit.Name = &line
	}
	if aisleName != "" {
		a, terr := s.aisleByName(ctx, aisleName)
		if terr != nil {
			return terr
		}
		edit.Edit.Aisle = &paprika.Aisle{UID: a.UID, Name: a.Name}
	}
	if listUID != "" && listUID != current.ListUID {
		if _, terr := s.groceryList(ctx, listUID); terr != nil {
			return terr
		}
		edit.Edit.List = &paprika.Place{ListUID: listUID, OrderFlag: nextGroceryOrder(existing, listUID)}
	}
	after := applyGroceryEdit(current, edit.Edit)
	before, result := groceryEntry(current), groceryEntry(after)
	if result == before {
		return &ToolError{Code: "duplicate", Message: "nothing to change: the item already is as requested"}
	}
	if dup := groceryCollision(existing, current, after); dup != nil && !in.AllowDuplicate {
		return &ToolError{Code: "duplicate", Message: fmt.Sprintf("%q is already on that list, not yet purchased (item uid %s); pass allow_duplicate=true to have it twice",
			dup.Name, dup.UID)}
	}
	names, err := s.groceryListNames(ctx)
	if err != nil {
		return fromErr(err)
	}
	var parts []string
	if result.Name != before.Name {
		parts = append(parts, fmt.Sprintf("change it to %q", result.Name))
	}
	// The new line shows a new amount, except when the line stays as it was or the amount is cleared.
	if result.Quantity != before.Quantity && (result.Name == before.Name || result.Quantity == "") {
		parts = append(parts, map[bool]string{true: "clear the stored amount", false: fmt.Sprintf("set the stored amount to %q", result.Quantity)}[result.Quantity == ""])
	}
	if result.Ingredient != before.Ingredient && result.Name == before.Name {
		parts = append(parts, fmt.Sprintf("set the ingredient to %q", result.Ingredient))
	}
	if result.Instruction != before.Instruction {
		parts = append(parts, map[bool]string{true: "remove the note", false: fmt.Sprintf("set the note to %q", result.Instruction)}[result.Instruction == ""])
	}
	if result.Aisle != before.Aisle {
		parts = append(parts, "put it in the aisle "+result.Aisle)
	}
	if result.ListUID != before.ListUID {
		parts = append(parts, fmt.Sprintf("move it to the list %q", names[result.ListUID]))
	}
	if result.Purchased != before.Purchased {
		parts = append(parts, map[bool]string{true: "tick it off as bought", false: "put it back on the list as not bought"}[result.Purchased])
	}
	edit.Before = before
	p := payload{GroceryEdit: &edit, AllowDuplicates: in.AllowDuplicate,
		Summary: fmt.Sprintf("Change the item %q on the Paprika grocery list %q: %s. This overwrites the item.",
			before.Name, names[before.ListUID], strings.Join(parts, "; "))}
	view, terr := s.saveProposal(ctx, kindGroceryEdit, p)
	if terr != nil {
		return terr
	}
	view.GroceryAfter, view.GroceryBefore = []GroceryEntry{result}, []GroceryEntry{before}
	out.Proposal = view
	return nil
}

// commitGroceryEdit reads the grocery items from Paprika, overwrites the edited
// fields of the item on that copy and reads it back.
func (s *server) commitGroceryEdit(ctx context.Context, p payload, out *CommitOut) *ToolError {
	edit := *p.GroceryEdit
	raws, items, terr := pullRows[paprika.GroceryItem](ctx, s, paprika.Groceries)
	if terr != nil {
		return terr
	}
	current, ok := findGrocery(items, edit.UID)
	if !ok {
		return notFound("the grocery item is no longer in Paprika; it was removed in the meantime")
	}
	want := applyGroceryEdit(current, edit.Edit)
	if groceryEntry(current) != groceryEntry(want) { // otherwise an earlier attempt already landed
		// The new line was built from the wording shown at proposal time; written
		// over a rename made since, it would undo it and disagree with the row.
		if edit.Edit.Name != nil && (current.Name != edit.Before.Name || current.Ingredient != edit.Before.Ingredient || current.Quantity != edit.Before.Quantity) {
			return &ToolError{Code: "uid_conflict", Message: fmt.Sprintf("nothing was written: the item was changed to %q in the meantime; propose the edit again", current.Name)}
		}
		if dup := groceryCollision(items, current, want); dup != nil && !p.AllowDuplicates {
			return &ToolError{Code: "duplicate", Message: "nothing was written: the same item was added to that list in the meantime (item uid " + dup.UID + ")"}
		}
		if terr := s.groceryTargetsExist(ctx, edit.Edit); terr != nil {
			return terr
		}
		if edit.Edit.List != nil { // the end of the list as it is now, not as it was when proposing
			place := paprika.Place{ListUID: edit.Edit.List.ListUID, OrderFlag: nextGroceryOrder(items, edit.Edit.List.ListUID)}
			edit.Edit.List = &place
		}
		if err := s.Client.EditGroceryItems(ctx, []paprika.GroceryRowEdit{{Current: raws[edit.UID], Edit: edit.Edit}}); err != nil {
			return writeFailed(err)
		}
		if _, items, terr = pullRows[paprika.GroceryItem](ctx, s, paprika.Groceries); terr != nil {
			return notVerified(terr.Message)
		}
	}
	got, ok := findGrocery(items, edit.UID)
	if !ok {
		return notVerified(fmt.Sprintf("Paprika accepted the change but no longer returns %q (item uid %s)", edit.Before.Name, edit.UID))
	}
	if groceryEntry(got) != groceryEntry(want) {
		return notVerified(fmt.Sprintf("Paprika accepted the change but returns %q not as edited", got.Name))
	}
	out.Items, out.GroceryBefore = []GroceryEntry{groceryEntry(got)}, []GroceryEntry{edit.Before}
	out.Sync = s.Sync.Freshness(ctx, string(paprika.Groceries))
	return nil
}

// groceryTargetsExist checks in Paprika that the list and the aisle an edit
// moves the item to were not removed since the proposal: an item written onto
// a removed list would vanish from every list shown.
func (s *server) groceryTargetsExist(ctx context.Context, e paprika.GroceryEdit) *ToolError {
	type target struct {
		col       paprika.Collection
		uid, what string
	}
	var targets []target
	if e.List != nil {
		targets = append(targets, target{paprika.GroceryLists, e.List.ListUID, "the grocery list"})
	}
	if e.Aisle != nil {
		targets = append(targets, target{paprika.GroceryAisles, e.Aisle.UID, "the aisle " + e.Aisle.Name})
	}
	for _, t := range targets {
		raws, _, terr := pullRows[json.RawMessage](ctx, s, t.col)
		if terr != nil {
			return terr
		}
		if _, ok := raws[t.uid]; !ok {
			return notFound(fmt.Sprintf("nothing was written: %s the item was to move to was removed in the meantime; propose the edit again", t.what))
		}
	}
	return nil
}

type ProposeGroceryCheckOffIn struct {
	ItemUIDs       []string `json:"item_uids" jsonschema:"uids of the 1 to 25 grocery items to tick off, from list_grocery_items"`
	Undo           bool     `json:"undo,omitempty" jsonschema:"true puts the items back on the list as not bought instead; default false"`
	AllowDuplicate bool     `json:"allow_duplicate,omitempty" jsonschema:"with undo: allow putting back an item that is on its list again, not yet purchased; default false"`
}

// groceryCheckOff is what a proposal stores.
type groceryCheckOff struct {
	Purchased bool           `json:"purchased"`
	Before    []GroceryEntry `json:"before"` // as shown to the user when proposing
}

func (s *server) proposeGroceryCheckOff(ctx context.Context, in ProposeGroceryCheckOffIn, out *ProposeOut) *ToolError {
	uids, terr := uidList("item_uids", in.ItemUIDs, maxGroceryItems, "list_grocery_items")
	if terr != nil {
		return terr
	}
	if terr := s.ensureFresh(ctx, &out.Meta, string(paprika.GroceryLists), string(paprika.Groceries)); terr != nil {
		return terr
	}
	existing, err := store.Load[paprika.GroceryItem](ctx, s.Store, paprika.Groceries)
	if err != nil {
		return fromErr(err)
	}
	purchased := !in.Undo
	check := groceryCheckOff{Purchased: purchased}
	var skipped []SkippedItem
	var after []GroceryEntry
	var lines []string
	for _, uid := range uids {
		item, ok := findGrocery(existing, uid)
		if !ok {
			return notFound("no grocery item with uid " + uid + "; see list_grocery_items")
		}
		if item.Purchased == purchased {
			skipped = append(skipped, SkippedItem{Name: item.Name, ExistingUID: item.UID,
				Reason: map[bool]string{true: "already ticked off", false: "not ticked off"}[purchased]})
			continue
		}
		was := item
		item.Purchased = purchased
		if dup := groceryCollision(existing, was, item); dup != nil && !in.AllowDuplicate {
			skipped = append(skipped, SkippedItem{Name: item.Name, ExistingUID: dup.UID,
				Reason: "already on the list again, not yet purchased; pass allow_duplicate=true to have it twice"})
			continue
		}
		setGrocery(existing, item) // a later item of this request must not duplicate it either
		check.Before = append(check.Before, groceryEntry(was))
		after, lines = append(after, groceryEntry(item)), append(lines, item.Name)
	}
	if len(check.Before) == 0 {
		return &ToolError{Code: "duplicate", Message: "nothing to change: every item already is as requested or would be on its list twice"}
	}
	names, err := s.groceryListNames(ctx)
	if err != nil {
		return fromErr(err)
	}
	verb := "Tick off %d item(s) as bought on the Paprika grocery list %s: %s."
	if in.Undo {
		verb = "Put %d item(s) back on the Paprika grocery list %s as not bought: %s."
	}
	p := payload{GroceryCheckOff: &check, Skipped: skipped, AllowDuplicates: in.AllowDuplicate,
		Summary: fmt.Sprintf(verb, len(lines), listsOf(check.Before, names), strings.Join(lines, "; "))}
	view, terr := s.saveProposal(ctx, kindGroceryCheckOff, p)
	if terr != nil {
		return terr
	}
	view.GroceryAfter, view.GroceryBefore, view.Skipped = after, check.Before, skipped
	out.Proposal = view
	return nil
}

// commitGroceryCheckOff sets purchased on the proposed items Paprika still
// holds, and reports as skipped the ones removed in the meantime and, when
// putting items back, the ones whose twin was added to their list since.
func (s *server) commitGroceryCheckOff(ctx context.Context, p payload, out *CommitOut) *ToolError {
	check := *p.GroceryCheckOff
	raws, items, terr := pullRows[paprika.GroceryItem](ctx, s, paprika.Groceries)
	if terr != nil {
		return terr
	}
	skipped, collided := p.Skipped, 0
	var expect []GroceryEntry // as shown when proposing
	var send []paprika.GroceryRowEdit
	for _, it := range check.Before {
		current, ok := findGrocery(items, it.UID)
		if !ok {
			skipped = append(skipped, SkippedItem{Name: it.Name, Reason: "removed from the list in the meantime"})
			continue
		}
		if current.Purchased != check.Purchased { // otherwise an earlier attempt, or someone in the app, already did it
			want := current
			want.Purchased = check.Purchased
			if dup := groceryCollision(items, current, want); dup != nil && !p.AllowDuplicates {
				skipped = append(skipped, SkippedItem{Name: it.Name, ExistingUID: dup.UID, Reason: "the same item was added to its list in the meantime"})
				collided++
				continue
			}
			setGrocery(items, want)
			send = append(send, paprika.GroceryRowEdit{Current: raws[it.UID], Edit: paprika.GroceryEdit{Purchased: &check.Purchased}})
		}
		expect = append(expect, it)
	}
	if len(expect) == 0 {
		if collided > 0 {
			return &ToolError{Code: "duplicate", Message: "nothing was written: every proposed item was removed, or added to its list again, in the meantime"}
		}
		return notFound("nothing was written: every proposed item was removed from the list in the meantime")
	}
	if len(send) > 0 {
		if err := s.Client.EditGroceryItems(ctx, send); err != nil {
			return writeFailed(err)
		}
		if _, items, terr = pullRows[paprika.GroceryItem](ctx, s, paprika.Groceries); terr != nil {
			return notVerified(terr.Message)
		}
	}
	for _, b := range expect {
		got, ok := findGrocery(items, b.UID)
		if !ok {
			return notVerified(fmt.Sprintf("Paprika accepted the change but no longer returns %q (item uid %s)", b.Name, b.UID))
		}
		if got.Purchased != check.Purchased {
			return notVerified(fmt.Sprintf("Paprika accepted the change but still returns %q unchanged", got.Name))
		}
		// paired with items: a skipped item has no before here
		out.Items, out.GroceryBefore = append(out.Items, groceryEntry(got)), append(out.GroceryBefore, b)
	}
	out.Skipped = skipped
	out.Sync = s.Sync.Freshness(ctx, string(paprika.Groceries))
	return nil
}

type ProposeGroceryRemovalIn struct {
	ItemUIDs           []string `json:"item_uids,omitempty" jsonschema:"uids of the 1 to 25 grocery items to remove, from list_grocery_items; not together with purchased_on_list_uid"`
	PurchasedOnListUID string   `json:"purchased_on_list_uid,omitempty" jsonschema:"remove every item already ticked off on this grocery list, as Paprika's own clear-purchased does; not together with item_uids"`
}

// maxClearPurchased bounds one clear-purchased proposal; a longer history takes several.
const maxClearPurchased = 200

func (s *server) proposeGroceryRemoval(ctx context.Context, in ProposeGroceryRemovalIn, out *ProposeOut) *ToolError {
	listUID := strings.TrimSpace(in.PurchasedOnListUID)
	if (len(in.ItemUIDs) == 0) == (listUID == "") {
		return invalid("give exactly one of item_uids or purchased_on_list_uid")
	}
	var uids []string
	if listUID == "" {
		var terr *ToolError
		if uids, terr = uidList("item_uids", in.ItemUIDs, maxGroceryItems, "list_grocery_items"); terr != nil {
			return terr
		}
	}
	if terr := s.ensureFresh(ctx, &out.Meta, string(paprika.GroceryLists), string(paprika.Groceries)); terr != nil {
		return terr
	}
	existing, err := store.Load[paprika.GroceryItem](ctx, s.Store, paprika.Groceries)
	if err != nil {
		return fromErr(err)
	}
	names, err := s.groceryListNames(ctx)
	if err != nil {
		return fromErr(err)
	}
	var p payload
	more := 0
	if listUID != "" {
		p.ClearPurchased = true
		if _, terr := s.groceryList(ctx, listUID); terr != nil {
			return terr
		}
		for _, it := range existing {
			if it.ListUID != listUID || !it.Purchased {
				continue
			}
			if len(p.GroceryRemoval) == maxClearPurchased {
				more++
				continue
			}
			p.GroceryRemoval = append(p.GroceryRemoval, groceryEntry(it))
		}
		if len(p.GroceryRemoval) == 0 {
			return &ToolError{Code: "duplicate", Message: fmt.Sprintf("nothing to remove: no item on %q is ticked off", names[listUID])}
		}
	}
	for _, uid := range uids {
		it, ok := findGrocery(existing, uid)
		if !ok {
			return notFound("no grocery item with uid " + uid + "; see list_grocery_items")
		}
		p.GroceryRemoval = append(p.GroceryRemoval, groceryEntry(it))
	}
	var lines []string
	for _, it := range p.GroceryRemoval {
		lines = append(lines, it.Name)
	}
	what := strings.Join(lines, "; ")
	if listUID != "" {
		what = "every ticked-off item (" + shortList(lines, 30) + ")"
	}
	p.Summary = fmt.Sprintf("REMOVE %d item(s) from the Paprika grocery list %s: %s. They disappear from the list on every device.",
		len(p.GroceryRemoval), listsOf(p.GroceryRemoval, names), what)
	if more > 0 {
		p.Summary += fmt.Sprintf(" %d more ticked-off item(s) stay; propose again to remove them.", more)
	}
	view, terr := s.saveProposal(ctx, kindGroceryRemoval, p)
	if terr != nil {
		return terr
	}
	view.GroceryGone = p.GroceryRemoval
	out.Proposal = view
	return nil
}

// commitGroceryRemoval removes the proposed items that Paprika still holds and
// checks that none of them is returned any more. Clearing what is ticked off
// leaves alone an item someone put back on the list since the proposal.
func (s *server) commitGroceryRemoval(ctx context.Context, p payload, out *CommitOut) *ToolError {
	rows := make([]removal, 0, len(p.GroceryRemoval))
	for _, it := range p.GroceryRemoval {
		rows = append(rows, removal{it.UID, it.Name})
	}
	var keep func(paprika.Raw) bool
	if p.ClearPurchased {
		list := map[string]string{}
		for _, it := range p.GroceryRemoval {
			list[it.UID] = it.ListUID
		}
		keep = func(r paprika.Raw) bool {
			it, err := paprika.Decode[paprika.GroceryItem](r.JSON)
			return err != nil || !it.Purchased || it.ListUID != list[it.UID] // a row that cannot be read is kept
		}
	}
	kept, terr := removeRows(ctx, s, paprika.Groceries, rows, keep, s.Client.RemoveGroceryItems)
	if terr != nil {
		return terr
	}
	for _, it := range p.GroceryRemoval {
		if kept[it.UID] {
			out.Skipped = append(out.Skipped, SkippedItem{Name: it.Name, ExistingUID: it.UID, Reason: "put back on the list as not bought, moved to another list, or no longer readable, in the meantime"})
			continue
		}
		out.GroceryGone = append(out.GroceryGone, it)
	}
	if len(out.GroceryGone) == 0 {
		return &ToolError{Code: "duplicate", Message: "nothing was written: every proposed item was put back on the list as not bought, moved to another list, or no longer readable, in the meantime"}
	}
	out.Sync = s.Sync.Freshness(ctx, string(paprika.Groceries))
	return nil
}

// removeBatch bounds the rows one removal request posts.
const removeBatch = 25

// removal names a row to remove: its uid, and how the user knows it.
type removal struct{ UID, Name string }

// removeRows posts back, marked deleted, each named row Paprika still holds,
// then reads the collection again and checks that none of them is returned.
// A row already gone is left alone: that is the goal. A row keep reports true
// for is left in place too, and returned among the kept uids.
func removeRows(ctx context.Context, s *server, col paprika.Collection, rows []removal, keep func(paprika.Raw) bool,
	remove func(context.Context, []paprika.Raw) error) (map[string]bool, *ToolError) {
	raws, _, terr := pullRows[json.RawMessage](ctx, s, col)
	if terr != nil {
		return nil, terr
	}
	kept := map[string]bool{}
	var send []paprika.Raw
	for _, row := range rows {
		raw, present := raws[row.UID]
		switch {
		case !present:
		case keep != nil && keep(raw):
			kept[row.UID] = true
		default:
			send = append(send, raw)
		}
	}
	if len(send) > 0 {
		// At most removeBatch rows per request, the size every other write keeps to. When
		// one fails, the rows sent before it are gone; a repeated commit skips them.
		for len(send) > 0 {
			n := min(len(send), removeBatch)
			if err := remove(ctx, send[:n]); err != nil {
				return nil, writeFailed(err)
			}
			send = send[n:]
		}
		if raws, _, terr = pullRows[json.RawMessage](ctx, s, col); terr != nil {
			return nil, notVerified(terr.Message)
		}
	}
	for _, row := range rows {
		if _, present := raws[row.UID]; present && !kept[row.UID] {
			return nil, notVerified(fmt.Sprintf("Paprika accepted the removal but still returns %q (uid %s)", row.Name, row.UID))
		}
	}
	return kept, nil
}

// pullRows reads a collection from Paprika: the raw rows by uid, and decoded.
func pullRows[T any](ctx context.Context, s *server, col paprika.Collection) (map[string]paprika.Raw, []T, *ToolError) {
	rows, err := s.Sync.Pull(ctx, col, nil)
	if err != nil {
		return nil, nil, fromErr(err)
	}
	raws, out := make(map[string]paprika.Raw, len(rows)), make([]T, 0, len(rows))
	for _, r := range rows {
		v, err := paprika.Decode[T](r.JSON)
		if err != nil {
			return nil, nil, fromErr(err)
		}
		raws[r.UID], out = r, append(out, v)
	}
	return raws, out, nil
}

// uidList trims and de-duplicates the uids a removal or check-off names.
func uidList(field string, in []string, max int, from string) ([]string, *ToolError) {
	if len(in) == 0 || len(in) > max {
		return nil, invalid(fmt.Sprintf("%s must hold 1 to %d entries; get them from %s", field, max, from))
	}
	var out []string
	seen := map[string]bool{}
	for i, raw := range in {
		uid := strings.TrimSpace(raw)
		if uid == "" {
			return nil, invalid(fmt.Sprintf("%s[%d] is empty", field, i))
		}
		if !seen[uid] {
			seen[uid] = true
			out = append(out, uid)
		}
	}
	return out, nil
}

func applyGroceryEdit(it paprika.GroceryItem, e paprika.GroceryEdit) paprika.GroceryItem {
	if e.Name != nil {
		it.Name = *e.Name
	}
	if e.Ingredient != nil {
		it.Ingredient = *e.Ingredient
	}
	if e.Quantity != nil {
		it.Quantity = *e.Quantity
	}
	if e.Instruction != nil {
		it.Instruction = *e.Instruction
	}
	if e.Aisle != nil {
		uid := e.Aisle.UID
		it.Aisle, it.AisleUID = e.Aisle.Name, &uid
	}
	if e.List != nil {
		it.ListUID, it.OrderFlag = e.List.ListUID, e.List.OrderFlag
	}
	if e.Purchased != nil {
		it.Purchased = *e.Purchased
	}
	return it
}

// groceryLine splits the item's line into its amount and what to buy, as the
// user wrote them ("2 l" and "Milk"), so that a new quantity replaces the old
// one and a new name keeps it:
//   - the stored quantity at the front of the line is cut, ignoring case and
//     spacing ("2l milk" for "2 l");
//   - with no stored quantity, a number the line starts with is cut only when
//     what follows is the ingredient Paprika derived ("2 lemons" for lemons),
//     so a number that belongs to the name stays ("7 Up", "00 flour");
//   - a stored quantity elsewhere in the line ("Milk (2 l)") cannot be cut
//     safely, so the bare ingredient stands for what to buy then;
//   - a line that holds no amount at all is what to buy, word for word.
func groceryLine(it paprika.GroceryItem) (amount, what string) {
	line, qty := strings.TrimSpace(it.Name), strings.TrimSpace(it.Quantity)
	switch {
	case line == "":
		what = it.Ingredient
	case qty != "":
		if rest, ok := cutAmount(line, qty); ok {
			amount, what = qty, rest
		} else if containsAmount(line, qty) && it.Ingredient != "" {
			amount, what = qty, it.Ingredient
		} else {
			amount, what = qty, line
		}
	default:
		if rest, ok := cutLeadingAmount(line); ok && it.Ingredient != "" && strings.HasPrefix(norm(rest), norm(it.Ingredient)) {
			amount, what = strings.TrimSpace(line[:len(line)-len(rest)]), rest
		} else {
			what = line
		}
	}
	if what == "" {
		what = it.Ingredient
	}
	return amount, what
}

// containsAmount tells whether amount appears in line as words of its own,
// ignoring case and spacing: "Milk (2 l)" holds "2 l", "Milk 12%" does not hold "2".
func containsAmount(line, amount string) bool {
	parts := strings.Fields(amount)
	for i, p := range parts {
		parts[i] = regexp.QuoteMeta(p)
	}
	re, err := regexp.Compile(`(?i)(?:^|[^\pL\pN])` + strings.Join(parts, `\s*`) + `(?:$|[^\pL\pN])`)
	return err == nil && len(parts) > 0 && re.MatchString(line)
}

// compact is v in lower case without any spaces.
func compact(v string) string { return strings.ToLower(strings.Join(strings.Fields(v), "")) }

// cutAmount removes amount from the front of line, comparing without case or
// spaces; the amount must end at a word boundary.
func cutAmount(line, amount string) (string, bool) {
	want := compact(amount)
	if want == "" {
		return "", false
	}
	got := ""
	for i, r := range line {
		if len(got) == len(want) {
			if !unicode.IsSpace(r) {
				return "", false
			}
			return strings.TrimSpace(line[i:]), true
		}
		if unicode.IsSpace(r) {
			continue
		}
		got += strings.ToLower(string(r))
		if !strings.HasPrefix(want, got) {
			return "", false
		}
	}
	return "", len(got) == len(want) // the line is the amount alone
}

// cutLeadingAmount removes a number and a unit the line starts with, as in
// "2 lemons" or "500g flour", and keeps the line when what follows the number
// is neither a space nor a unit ("7up").
func cutLeadingAmount(line string) (string, bool) {
	parsed := ingredients.Parse(line)
	if len(parsed) != 1 || parsed[0].Quantity == "" || parsed[0].Item == "" {
		return "", false
	}
	rest := strings.TrimPrefix(line, parsed[0].Quantity)
	if u := parsed[0].Unit; u != "" {
		rest = strings.TrimPrefix(strings.TrimSpace(rest), u)
	} else if r, _ := utf8.DecodeRuneInString(rest); !unicode.IsSpace(r) { // also a no-break space; RuneError when rest is empty
		return "", false
	}
	return strings.TrimSpace(rest), true
}

// groceryCollision returns an unpurchased item an edit would newly duplicate on
// its list: only an edit that renames, moves or un-ticks the item can do that.
// An item the edited one already duplicated before the edit is not new: a
// change of amount or wording must not be refused over it.
func groceryCollision(existing []paprika.GroceryItem, before, after paprika.GroceryItem) *paprika.GroceryItem {
	if after.Purchased {
		return nil
	}
	if norm(before.Name) == norm(after.Name) && before.ListUID == after.ListUID && !before.Purchased {
		return nil
	}
	was := paprika.NewGroceryItem{UID: before.UID, Name: before.Name, Ingredient: before.Ingredient}
	is := paprika.NewGroceryItem{UID: after.UID, Name: after.Name, Ingredient: after.Ingredient}
	stays := !before.Purchased && before.ListUID == after.ListUID
	for i, e := range existing {
		if e.ListUID != after.ListUID || e.Purchased || e.UID == after.UID || !sameGrocery(e, is) {
			continue
		}
		if !stays || !sameGrocery(e, was) {
			return &existing[i]
		}
	}
	return nil
}

// setGrocery replaces the item with the same uid in items.
func setGrocery(items []paprika.GroceryItem, it paprika.GroceryItem) {
	for i := range items {
		if items[i].UID == it.UID {
			items[i] = it
		}
	}
}

func nextGroceryOrder(existing []paprika.GroceryItem, listUID string) int {
	order := 0
	for _, e := range existing {
		if e.ListUID == listUID && e.OrderFlag >= order {
			order = e.OrderFlag + 1
		}
	}
	return order
}

func (s *server) aisleByName(ctx context.Context, name string) (paprika.GroceryAisle, *ToolError) {
	aisles, err := store.Load[paprika.GroceryAisle](ctx, s.Store, paprika.GroceryAisles)
	if err != nil {
		return paprika.GroceryAisle{}, fromErr(err)
	}
	var known []string
	for _, a := range aisles {
		if norm(a.Name) == norm(name) {
			return a, nil
		}
		known = append(known, a.Name)
	}
	return paprika.GroceryAisle{}, notFound(fmt.Sprintf("no aisle named %q; Paprika has: %s", name, strings.Join(known, ", ")))
}

func (s *server) groceryListNames(ctx context.Context) (map[string]string, error) {
	lists, err := store.Load[paprika.GroceryList](ctx, s.Store, paprika.GroceryLists)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(lists))
	for _, l := range lists {
		names[l.UID] = l.Name
	}
	return names, nil
}

// listsOf names, quoted and in order of first appearance, the lists the items are on.
func listsOf(items []GroceryEntry, names map[string]string) string {
	var lists []string
	seen := map[string]bool{}
	for _, it := range items {
		if !seen[it.ListUID] {
			seen[it.ListUID] = true
			lists = append(lists, fmt.Sprintf("%q", names[it.ListUID]))
		}
	}
	return strings.Join(lists, ", ")
}

// shortList joins at most n lines and says how many more there are.
func shortList(lines []string, n int) string {
	if len(lines) <= n {
		return strings.Join(lines, "; ")
	}
	return fmt.Sprintf("%s; and %d more", strings.Join(lines[:n], "; "), len(lines)-n)
}
