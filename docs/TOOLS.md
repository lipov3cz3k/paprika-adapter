# MCP tools

Exact JSON Schemas (input and output, as served by `tools/list`) are in [tools.json](tools.json).
A test fails if that file drifts from the code; regenerate with `make tools`.

The tools are served over streamable HTTP at `/mcp` and need an `Authorization: Bearer <token>` header
([INSTALL.md](INSTALL.md#2-credentials-and-client-tokens)); without one the answer is HTTP 401, before any MCP exchange.
Some clients prefix the names with the server's name, for example `mcp_paprika_<tool>`.

## Common result shape

Every tool returns structured JSON with the same head:

```json
{
  "ok": true,
  "sync": [
    {"collection": "groceries", "state": "fresh", "last_synced_at": "2026-10-08T16:58:48Z", "age_seconds": 0, "rows": 6}
  ]
}
```

| Field | Meaning |
|---|---|
| `ok` | `false` means the call failed; `error` says why and MCP `isError` is set |
| `error.code` | stable code, see below |
| `error.retryable` | `true` if repeating the same call later may work without anyone changing anything |
| `sync[].state` | `fresh`: confirmed against Paprika within `PAPRIKA_MAX_AGE`. `stale`: cached copy, could not be confirmed. `never_synced`: no complete copy cached yet (a first recipe sync may be part-way through; `rows` shows how far). Tools refuse to answer from it |
| `sync[].error` | why the last sync attempt failed (`code`, `message`, `at`) |
| `sync[].protocol_drift` | fields Paprika added (`unknown_fields`) or dropped (`missing_fields`) |

A read with `ok: true` and `state: stale` is real data that may be out of date. The agent should say so.

Conventions: uids are Paprika's own and stable. Adapter timestamps are RFC 3339 UTC. Calendar dates are
`YYYY-MM-DD`. A recipe's `created` is passed through as Paprika stores it (`YYYY-MM-DD HH:MM:SS`, zone unspecified).

### Error codes

| Code | Meaning | Retry |
|---|---|---|
| `invalid_input` | argument missing or malformed; nothing was sent | no |
| `not_found` | no such recipe, list, meal type or proposal | no |
| `auth_failed` | Paprika rejected the credentials | no, see runbook |
| `upstream_unavailable` | network error, timeout, 429 or 5xx; or the first sync has not finished (it continues in the background and on each call) | yes |
| `protocol_changed` | Paprika's response no longer has the expected shape | no, see runbook |
| `plan_required` | Paprika refused the request because of the account's plan, typically a write after the subscription lapsed (reads keep working) | no, see runbook |
| `rejected` | Paprika refused the request | no |
| `duplicate` | the write would duplicate something already in Paprika | no |
| `uid_conflict` | Paprika holds something else under the proposal's uid than when it was proposed; nothing was written | propose again |
| `confirmation_required` | `commit_proposal` called without `confirm: true` | after approval |
| `approval_declined` | the user declined the approval prompt | no |
| `approval_unavailable` | approval is mandatory and the client cannot prompt, or the prompt failed | no |
| `proposal_expired` | proposals live 15 minutes | propose again |
| `tool_not_allowed` | the proposal's kind is left out of `PAPRIKA_TOOLS`; nothing was written | no |
| `write_not_verified` | written but not readable back yet | yes, repeat the commit |
| `uid_conflict` | Paprika already holds a different row under the minted uid; nothing overwritten | propose again |
| `cache_error` | local SQLite problem | no |

## Read tools (always available)

| Tool | Arguments | Returns |
|---|---|---|
| `search_recipes` | `query?` (all words must match, prefix match, over name, ingredients, categories, description, notes, source), `category?`, `limit?` (1-50, default 10), `include_trashed?` | `recipes[]` (`uid`, `name`, `categories`, `rating`, `favorite`, times, `source`), `total_matches`, `truncated` |
| `get_recipe` | `uid` | `recipe` with `ingredients_raw` (verbatim from Paprika, authoritative), `ingredients_parsed[]` (best effort: `raw`, `quantity`, `unit`, `item`, `note`, `heading`), `directions`, `categories[]`, `hash`, `cached_at` |
| `get_recipe_photo` | `uid`, `size?` (`thumbnail` default, or `full`), `index?` (with `full`: 1 to `gallery_photos`; default the cover) | an **image content block** with the photo, and `photo` describing it (`recipe`, `size`, `index`, `photo_uid`, `mime_type`, `bytes`, `width`, `height`, `gallery_photos`) |
| `list_pantry` | `query?`, `in_stock_only?`, `limit?` (1-500, default 100) | `items[]` (`uid`, `ingredient`, `quantity`, `aisle`, `in_stock`, `purchase_date`, `expiration_date`), `total`, `truncated` |
| `get_meal_plan` | `from?`, `to?` (default: Monday to Sunday of the current week in `PAPRIKA_TZ`; max 92 days) | `from`, `to`, `today`, `timezone`, `meals[]` (`uid`, `date`, `meal_type`, `meal_type_uid`, `name`, `recipe_uid`), `meal_types[]` |
| `list_grocery_lists` | none | `lists[]` (`uid`, `name`, `is_default`, `items`, `unpurchased`) |
| `list_grocery_items` | `list_uid`, `include_purchased?`, `limit?` (1-500, default 200) | `list`, `items[]` (`uid`, `name`, `ingredient`, `quantity`, `instruction`, `aisle`, `list_uid`, `purchased`, `recipe`, `recipe_uid`), `total`, `truncated` |
| `sync_status` | `refresh?` (`true` contacts Paprika now) | `healthy`, `writes_enabled`, `paprika_host`, `adapter_version`, and `sync[]` for all ten collections |

### Photos

`get_recipe` reports `has_photo` and `gallery_photos`. A recipe can have a cover thumbnail (small, a few tens
of kilobytes; most recipes with a picture have only this) and a gallery of full-resolution photos (several
hundred kilobytes each; step-by-step pictures live here).

- The image arrives as MCP image content next to a text copy of the JSON result. Whether the model sees it, or
  the user is shown it, depends on the MCP client; a client that ignores image content gets only the description.
- Photos are not cached. Each call asks Paprika for a download link, which is valid for about ten minutes, and
  fetches the image, so the tool needs Paprika to be reachable: otherwise `upstream_unavailable`.
- `not_found` means the recipe has no photo of that kind; `size: full` on a recipe with only a thumbnail says so.
- A full-resolution photo is a large tool result. Ask for `thumbnail` unless the detail matters.
- Download links are never returned or logged. Photos larger than 8 MiB are refused (`rejected`).

`ingredients_parsed` never replaces `ingredients_raw`: joining every `raw` with newlines gives back the stored
text minus blank lines.

## Write tools (only with `PAPRIKA_ENABLE_WRITES=true`)

There are exactly fourteen: thirteen that prepare a write and one that performs it. Four of the writes only
create new rows: meal-plan entries, grocery items, pantry items and recipes (`propose_recipe_create`). Five
change existing data: `propose_recipe_edit` (text fields, rating and categories of a recipe),
`propose_meal_plan_edit` (one meal-plan entry), `propose_grocery_edit` (one grocery item),
`propose_grocery_check_off` (whether grocery items are bought) and `propose_pantry_edit` (one pantry item).
`propose_recipe_trash` moves recipes into or out of Paprika's trash. Three remove data:
`propose_meal_plan_removal`, `propose_grocery_removal` and `propose_pantry_removal`. Grocery lists, aisles and
meal types cannot be created, changed or deleted; no recipe is ever deleted for good.

| Tool | Arguments | Effect |
|---|---|---|
| `propose_meal_plan_entry` | `date`, `meal_type_uid` (from `get_meal_plan.meal_types`), and exactly one of `recipe_uid` or `name`; `allow_duplicate?` | Writes nothing. Returns `proposal` (`proposal_id`, `summary`, `expires_at`, `meal`) |
| `propose_meal_plan_edit` | `meal_uid` (from `get_meal_plan`), and at least one of `date`, `meal_type_uid`, `recipe_uid` or `name`; `allow_duplicate?` | Writes nothing. Returns `proposal` (`proposal_id`, `summary`, `meal_before`, `meal`) |
| `propose_meal_plan_removal` | `meal_uids[]` 1-25 (from `get_meal_plan`) | Writes nothing. Returns `proposal` (`proposal_id`, `summary`, `meals_removed[]`) |
| `propose_grocery_items` | `list_uid` (no default list), `items[]` 1-25 of `{name, quantity?, instruction?}`; `allow_duplicates?` | Writes nothing. Returns `proposal` (`proposal_id`, `summary`, `items[]`, `skipped[]`) |
| `propose_grocery_edit` | `item_uid` (from `list_grocery_items`), and at least one of `name`, `quantity` (`""` clears), `instruction` (`""` clears), `aisle` (by name), `list_uid`, `purchased`; `allow_duplicate?` | Writes nothing. Returns `proposal` (`proposal_id`, `summary`, `grocery_before[]`, `grocery_after[]`, one item each) |
| `propose_grocery_check_off` | `item_uids[]` 1-25 (from `list_grocery_items`), `undo?`, `allow_duplicate?` | Writes nothing. Returns `proposal` (`proposal_id`, `summary`, `grocery_before[]`, `grocery_after[]`, `skipped[]`) |
| `propose_grocery_removal` | exactly one of `item_uids[]` 1-25, or `purchased_on_list_uid` | Writes nothing. Returns `proposal` (`proposal_id`, `summary`, `grocery_removed[]`) |
| `propose_pantry_items` | `items[]` 1-25 of `{ingredient, quantity?, expiration_date?}`; `allow_duplicates?` | Writes nothing. Returns `proposal` (`proposal_id`, `summary`, `pantry_items[]`, `skipped[]`). Items are created in stock with today's date (in `PAPRIKA_TZ`) as purchase date |
| `propose_pantry_edit` | `item_uid` (from `list_pantry`), and at least one of `ingredient`, `quantity` (`""` clears), `in_stock`, `expiration_date`, `clear_expiration`, `purchase_date`; `allow_duplicate?` | Writes nothing. Returns `proposal` (`proposal_id`, `summary`, `pantry_before[]`, `pantry_items[]`, one item each) |
| `propose_pantry_removal` | `item_uids[]` 1-25 (from `list_pantry`) | Writes nothing. Returns `proposal` (`proposal_id`, `summary`, `pantry_removed[]`) |
| `propose_recipe_edit` | `uid`, and at least one of `rating` (0-5, 0 = unrated), `name`, `ingredients`, `directions`, `description`, `notes`, `append_note`, `nutritional_info`, `servings`, `prep_time`, `cook_time`, `total_time`, `difficulty`, `source`, `source_url`, `categories` (replace), `add_categories`, `remove_categories`; `allow_duplicate?` | Writes nothing. Returns `proposal` (`proposal_id`, `summary`, `recipe_edit.changes[]` with `field`, `from`, `to`) |
| `propose_recipe_create` | `name`, and any of `ingredients`, `directions`, `description`, `notes`, `nutritional_info`, `servings`, `prep_time`, `cook_time`, `total_time`, `difficulty`, `source`, `source_url`, `categories[]` (by name), `rating`; `allow_duplicate?` | Writes nothing. Returns `proposal` (`proposal_id`, `summary`, `recipe_new` with the new uid and every field given) |
| `propose_recipe_trash` | `uids[]` 1-25 (from `search_recipes`), `restore?` | Writes nothing. Returns `proposal` (`proposal_id`, `summary`, `recipes_trashed[]`, `skipped[]`) |
| `commit_proposal` | `proposal_id`, `confirm: true` | Writes the proposal, reads it back, returns `status`, `verified`, and the rows as read back |

### How a write goes

1. **Propose.** Inputs are validated against data confirmed fresh just now (a stale cache refuses to propose).
   Uids are minted here and stored with the proposal in SQLite. Grocery items already on the list, unpurchased,
   under the same name are put in `skipped` instead of being proposed; so are pantry items whose ingredient
   is already in the pantry, in stock or not (restocking an existing item is `propose_pantry_edit` with `in_stock: true`).
2. **Approve.** The agent shows `summary` to the user. If the client supports MCP elicitation,
   `commit_proposal` also puts the same summary to the user directly and writes only on *accept*.
   With `PAPRIKA_APPROVAL=human` a client that cannot prompt cannot write at all.
   With `PAPRIKA_APPROVAL=none` this step does not exist: the adapter sends no prompt, its instructions tell
   the agent to commit without asking, and `confirm: true` is all a commit needs. Propose and commit stay
   two calls, because the stored proposal is what makes a retried commit safe.
3. **Commit.** The adapter re-reads the collection from Paprika, then:
   - rows whose uid is already there are not sent again;
   - rows that would now duplicate something added elsewhere are skipped (`duplicate` if nothing is left);
   - the rest go in one POST, which is never retried automatically.
4. **Read back.** The collection is fetched again and every row is checked by uid. Only then is the result
   `verified: true` and the proposal marked committed.

### Editing a recipe

`propose_recipe_edit` overwrites fields of a recipe. What that means:

- Every text field given **replaces** what the recipe has: `name`, `ingredients` (the whole list, one
  ingredient per line, as Paprika stores it), `directions`, `description`, `notes`, `nutritional_info`, and the
  one-line `servings`, `prep_time`, `cook_time`, `total_time`, `difficulty`, `source`, `source_url`.
  Times and servings are free text, as in the apps; write times in minutes (`90 min`), which is what the
  Paprika 4 editor parses back. Paprika 4 also keeps minutes and a servings range as numbers: an edit of the
  text clears them, and the apps go by the text. An empty string clears a field; `name`, `ingredients`
  and `directions` cannot be emptied.
- `append_note` instead adds a paragraph after the existing notes, and is the right choice for "add a note".
- `categories` replaces the recipe's categories (an empty list removes all); `add_categories` and
  `remove_categories` change only the ones named, applied to what the recipe has at commit time. Categories
  are named as in Paprika (case does not matter) and must exist: the adapter does not create categories.
- The proposal lists every change with the old and the new value. That is what the user should see before
  approving, and the commit result repeats it, so the old text can be put back with another edit.
- At commit the recipe is read from Paprika, the edited fields are overwritten on that copy, and the whole
  recipe is written back with a new hash, then read back. Fields the adapter does not know are
  carried over untouched.
- An edit made in an app to the **same field** between propose and commit is overwritten without warning; this
  was chosen deliberately. Edits to other fields survive. An edit made in an app during the fraction of a
  second between the adapter's read and its write would be lost whatever the field.
- Recipes in the trash cannot be edited. Proposing values the recipe already has answers `duplicate`.
- Renaming a recipe to the name of another recipe outside the trash answers `duplicate` unless
  `allow_duplicate` is set; the check is repeated at commit.
- Photos, favourite and pinned state, scale and the trash flag cannot be changed.

### Creating a recipe, and the trash

`propose_recipe_create` creates a recipe in the shape the Paprika 4 apps save one: the text fields given,
the rest empty, no photo, not a favourite, in the default cookbook, `created` now in `PAPRIKA_TZ`. Categories are named as in Paprika
and must exist. A recipe with the same name outside the trash is refused as `duplicate` unless
`allow_duplicate` is set. The uid is minted when proposing, so a repeated commit finds the recipe instead of
creating a second one; the new recipe is in the cache at once (searchable, editable).

`propose_recipe_trash` moves recipes to Paprika's trash, or with `restore: true` takes them out. Search
leaves trashed recipes out unless `include_trashed` is set, and they cannot be edited. Only an app can empty
the trash: this adapter never deletes a recipe for good. Recipes already in the asked-for state are listed in
`skipped`.

### Editing a meal-plan entry

`propose_meal_plan_edit` changes one existing entry, found by its `meal_uid`:

- `date` moves it to another day, `meal_type_uid` changes which meal of the day it is, `recipe_uid` or `name`
  replaces what is planned (a recipe, or free text). Any combination in one edit.
- The proposal shows the entry before (`meal_before`) and after (`meal`); the commit result repeats both, so
  an edit is undone by proposing the reverse.
- At commit the meal plan is read from Paprika, the edited fields are overwritten on that entry's row, and
  the row is written back and read back. Scale and anything else on the row is carried over. An edit made in
  an app to the same fields in the meantime is overwritten.
- If the entry was removed in an app before the commit, nothing is written (`not_found`).
- An edit that would repeat something already planned for that day and meal type is refused as `duplicate`
  unless `allow_duplicate` is set. Only one entry changes per proposal.

`propose_meal_plan_removal` removes the entries named in `meal_uids`, the same way `propose_pantry_removal`
removes pantry items (see below): named by uid only, the summary lists each entry with its day and meal type,
and the commit posts each row still in Paprika back marked deleted and checks that none is returned.
There is no restore tool: to undo, plan the entry again with `propose_meal_plan_entry`.

### Grocery items: editing, ticking off, removing

`propose_grocery_edit` changes one existing item, found by its `item_uid`:

- `name` replaces what to buy and `quantity` the amount; the line Paprika shows is rebuilt as
  `quantity name`. Giving only `quantity` keeps the item's own wording and drops the old amount: `Milk`
  becomes `2 l Milk`, `1l milk` becomes `2 l milk`, and when the old amount sits inside the line, as in
  `Milk (1 l)`, the bare ingredient is used: `2 l milk`. A number that is part of the name stays (`7 Up`
  becomes `2 l 7 Up`). Giving only `name` keeps the amount, even one written only in the line: `2 lemons`
  becomes `2 limes`. `instruction` replaces the note, `aisle` moves it to an existing aisle (by name),
  `list_uid` moves it to the end of another list (the end as it is at commit), `purchased` ticks it off or
  puts it back. Any combination.
- The proposal shows the item before (`grocery_before`) and after (`grocery_after`); the commit result
  repeats both (`grocery_before`, `items`).
- At commit the grocery items are read from Paprika and the edited fields are overwritten on that row; the
  recipe link and anything else on the row is carried over. If the item was removed meanwhile, nothing is
  written (`not_found`); if an edit of `name` or `quantity` finds the item renamed or given another amount
  meanwhile, nothing is written either (`uid_conflict`), as the new line was built from the old wording.
  Nor when the list or aisle the item is to move to was removed meanwhile (`not_found`).
- A result that would duplicate an unpurchased item with the same name on the target list (by renaming,
  moving or un-ticking) is refused as `duplicate` unless `allow_duplicate` is set. An item that already was
  on the list twice is not refused just for a changed amount or note.

`propose_grocery_check_off` is the "I bought these" tool: it ticks off up to 25 items, on any lists, in one
approval; `undo: true` puts them back on the list. Items already in that state are reported in `skipped`, and
so is an item `undo` would put back next to an unpurchased twin on its list, as the edit refuses it, unless
`allow_duplicate` is set. At commit, items removed in the meantime, or whose twin was added since, are skipped
as well, and the rest are written in one request.

`propose_grocery_removal` removes items for good: either the ones named in `item_uids`, or with
`purchased_on_list_uid` every ticked-off item of one list, like Paprika's own "clear purchased" (up to 200 per
proposal; the summary says when more remain, and the commit posts them 25 per request). Removal works like
the pantry removal below; when clearing ticked-off items, one put back on the list as not bought, or moved to
another list, before the commit stays and is reported in `skipped` (`duplicate` when that leaves nothing to
remove). To record that something was bought, tick it off instead: that keeps it in the list's history.

### Editing and removing pantry items

`propose_pantry_edit` changes one existing item, found by its `item_uid`:

- `quantity` replaces the amount (an empty string clears it), `in_stock` marks the item used up or back in
  stock, `expiration_date` sets and `clear_expiration` removes the expiration date, `purchase_date` sets the
  purchase date, `ingredient` renames the item. Any combination in one edit.
- "We are out of X" is `in_stock: false`, which keeps the item in the pantry. Removal is for items that
  should not be listed at all.
- The proposal shows the item before (`pantry_before`) and after (`pantry_items`); the commit result repeats
  both, so an edit is undone by proposing the reverse.
- At commit the pantry is read from Paprika, the edited fields are overwritten on that item's row, and the
  row is written back and read back. Aisle, notes and anything else on the row is carried over; a renamed
  item keeps its aisle. An edit made in an app to the same fields in the meantime is overwritten.
- If the item was removed in an app before the commit, nothing is written (`not_found`).
- Renaming to an ingredient another pantry item already has is refused as `duplicate` unless
  `allow_duplicate` is set.

`propose_pantry_removal` removes the items named in `item_uids`:

- Items are named by uid only: there is no "remove everything matching" and no "clear the pantry". The
  summary lists every item by name and quantity; that list is what the user approves.
- At commit the pantry is read from Paprika and each item still there is posted back marked deleted, the way
  the Paprika apps delete. Items already gone are left alone. The pantry is then read again and the result
  is `verified` only if none of the items is returned.
- The commit result lists the removed items as they were (`pantry_removed`). There is no restore tool: to
  undo, add the item again with `propose_pantry_items`. It gets a new uid, and notes it had are lost.

### Retrying

`commit_proposal` is safe to repeat in every state:

| Situation | What a repeat does |
|---|---|
| Already committed | returns the stored result with `status: already_committed`, no request to Paprika |
| POST failed before reaching Paprika | sends the same uids again |
| POST reached Paprika but the response was lost | finds the uids on re-read, sends nothing, verifies, reports `committed` |
| Same request proposed twice | the second commit finds the items and fails with `duplicate` |
| Meal-plan edit whose response was lost | finds the entry already changed, writes nothing, verifies |
| Recipe edit whose response was lost | finds the fields already changed, writes nothing, verifies; a note is not appended twice |

### Example

```json
// propose_grocery_items {"list_uid": "9E12…F6F6", "items": [{"name": "chicken thighs", "quantity": "1 lb"}]}
{
  "ok": true,
  "proposal": {
    "proposal_id": "3DABFD39-6697-4816-A9E3-6E2FF4AA4924",
    "kind": "grocery_items",
    "expires_at": "2026-10-08T17:13:48Z",
    "summary": "Add 1 item(s) to the Paprika grocery list \"My Grocery List\": 1 lb chicken thighs.",
    "list": {"uid": "9E12…F6F6", "name": "My Grocery List"},
    "items": [{"uid": "81ECA948-E0ED-4A76-980E-1FE11160E08E", "name": "1 lb chicken thighs", "quantity": "1 lb", "aisle": "Meat"}]
  }
}

// commit_proposal {"proposal_id": "3DABFD39-6697-4816-A9E3-6E2FF4AA4924", "confirm": true}
{
  "ok": true,
  "proposal_id": "3DABFD39-6697-4816-A9E3-6E2FF4AA4924",
  "kind": "grocery_items",
  "status": "committed",
  "verified": true,
  "committed_at": "2026-10-08T16:58:48Z",
  "list": {"uid": "9E12…F6F6", "name": "My Grocery List"},
  "items": [{"uid": "81ECA948-E0ED-4A76-980E-1FE11160E08E", "name": "1 lb chicken thighs", "ingredient": "chicken thighs", "quantity": "1 lb", "aisle": "Meat", "purchased": false}],
  "sync": [{"collection": "groceries", "state": "fresh", "last_synced_at": "2026-10-08T16:58:48Z", "age_seconds": 0, "rows": 6}]
}
```

(Captured from the demo run against the fake backend; uids shortened by hand.)
