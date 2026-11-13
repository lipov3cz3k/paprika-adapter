# Paprika sync protocol: what the adapter relies on, and how sure we are

Paprika publishes no API. Everything below is reverse-engineered, by other people or from the Paprika apps,
and re-checked only as far as stated. Treat it as unstable.

Scope: **Paprika 3 and 4** cloud sync (`/api/v3/sync/*`, before 2026-11-12 `/api/v2/sync/*`, plus the
`/api/v1/account/login/` login).
Paprika 4 (released 2026-11-06) is an in-place update of Paprika 3 on the same account. It syncs through
`/api/v3/sync/*`, which serves the same rows as v2 plus new collections (pantry locations, cookbooks, saved
searches).
Paprika 1/2 formats and `.paprikarecipes` files are not supported.

The document has four parts: [what the adapter sends](#operations), [what the Paprika 4 apps do](#what-the-paprika-4-apps-do),
[what was checked on a real account](#verified-against-a-real-account), and the
[facts the design depends on](#facts-the-design-depends-on).

## Evidence levels

| Level | Meaning |
|---|---|
| **C** | Wire capture of the Paprika 3.8.4 macOS client (mitmproxy, sanitised HAR) in `bojanrajkovic/mcp-paprika` at `b7de3b8` (captures dated 2026-05) |
| **T** | Described in `aarons22/paprika-tools` `API_REFERENCE.md` at `361b495`; its author reports live checks (meal writes: 2026-06-29) |
| **L** | Observed by this project against `www.paprikaapp.com` on 2026-10-08, without an account |
| **A** | Observed on a real account, see [Verified against a real account](#verified-against-a-real-account) |
| **P** | What the Paprika 4 apps do, see [What the Paprika 4 apps do](#what-the-paprika-4-apps-do). Shows what a client sends, not what the server accepts |
| **I** | Inferred from the above; nobody has confirmed it |

Fixture tests in this repo prove the adapter matches the shapes below. They prove nothing about Paprika.

## Operations

The paths below were captured on v2. Since 2026-11-12 the adapter sends the same requests to `/api/v3/sync/`;
for reads both answer identically (A), and the writes were checked on v3 on
[2026-11-14](#2026-11-14-writes-on-v3-paprika-4022-in-use).

| # | Operation | Request | Evidence | On a real account |
|---|---|---|---|---|
| 1 | Login | `POST /api/v1/account/login/`, form fields `email`, `password` → `{"result":{"token"}}` | C (code), T | **A** 2026-10-09 |
| 2 | Bad login | HTTP **200** with `{"error":{"code":0,"message":"Account not found."}}` | L | n/a |
| 3 | Missing/expired token | HTTP 401 with `{"error":{"code":0,"message":"Invalid or expired credentials."}}` | L | n/a |
| 4 | Change counters | `GET /api/v2/sync/status/` → `{"result":{"recipes":952,"groceries":1323,…}}` | C | **A** 2026-10-09 (request works). The apps treat the values as revisions (P) |
| 5 | Recipe list | `GET /sync/recipes/` → `[{uid, hash}]` | C, T | **A** 2026-10-09 |
| 6 | Recipe detail | `GET /sync/recipe/{uid}/` → 28-field object in the captures, 36 fields now | C, T | **A** 2026-10-09, 157 recipes |
| 7 | Unknown recipe | HTTP 200 with `{"error":{"message":"Recipe not found."}}` | C (code comment) | **A** 2026-10-09: reported as `not_found` |
| 8 | Categories, pantry, meals, meal types, grocery lists, grocery items, aisles, grocery ingredients | `GET /sync/{collection}/` → every row, unpaginated | C, T | **A** 2026-10-09 |
| 9 | Add grocery items | `POST /sync/groceries/`, multipart part `data` = gzip of a JSON **array** of complete objects → `{"result":true}` | C, T | **A** 2026-10-09 |
| 10 | Add meal-plan entry | `POST /sync/meals/`, same envelope. Paprika 4 also sends `is_ingredient` and `scale` (P); the adapter sends them as `false` and `null` | C, T, P | **A** 2026-10-09, by name and without the two keys, 2026-11-14 with them on v3; with `recipe_uid` 2026-11-14 |
| 11 | Add pantry items | `POST /sync/pantry/`, same envelope. Paprika 3 sent no `notes` and no `location_uid` (C); Paprika 4 sends both (P), and so does the adapter, as `null` | C, P | **A** 2026-10-09 without the two keys, 2026-11-14 with them on v3 |
| 12 | Malformed new uid | HTTP 200 with `{"error":{"message":"Invalid uid."}}` | C (docs) | not verified |
| 13 | Gallery photo list | `GET /sync/photos/` → `[{uid, recipe_uid, filename, name, order_flag, hash}]`, no link | docs of `mcp-paprika` | **A** 2026-10-09, 53 rows |
| 14 | Gallery photo link | `GET /sync/photo/{uid}/` → `{"result":{"photo_url"}}` | docs of `mcp-paprika` | **A** 2026-10-09 |
| 15 | Photo download | `GET` of a `photo_url`: a signed link to `s3.amazonaws.com/uploads.paprikaapp.com/…`, no API token | docs of `mcp-paprika` | **A** 2026-10-09: JPEG, thumbnail about 35 kB, gallery photo about 240 kB |
| 16 | Overwrite a recipe | `POST /sync/recipe/{uid}/`, multipart part `data` = gzip of **one** JSON object (not an array): the whole recipe, without `photo_url` and `on_grocery_list`, with `deleted` and a new `hash` → `{"result":true}` | C, P | **A** 2026-11-14 |
| 17 | Edit a meal-plan entry | `POST /sync/meals/` with the whole existing row under its uid, changed fields overwritten | C (whole-object upsert) | **A** 2026-10-09 |
| 18 | Edit a pantry item | `POST /sync/pantry/` with the whole existing row under its uid, changed fields overwritten | C (whole-object upsert) | **A** 2026-10-10 |
| 19 | Remove pantry items | `POST /sync/pantry/` with each whole existing row and `deleted: true` | C (soft delete) | **A** 2026-10-10 |
| 20 | Tell the other devices | `POST /sync/notify/` without a body, after a commit was written and read back. Paprika then sends the account's other devices an `update` event and they sync at once | P | **A** 2026-11-14: `{"result": true}`, and a recipe edit followed by the ping showed up at once in the open macOS and Android apps. A failure is logged and does not fail the commit |

Both `www.paprikaapp.com` and `paprikaapp.com` answered identically (L). The adapter uses `www`.

### Where the adapter deliberately differs from the Paprika apps

| Topic | Apps | Adapter | Why |
|---|---|---|---|
| Aisle for a new grocery item | always send an aisle | sends the aisle Paprika already remembers for that ingredient, otherwise `aisle: ""`, `aisle_uid: null` | no guessing; `paprika-tools` reports empty aisles work (T). Accepted and stored as sent (A 2026-10-09). The apps look the aisle up by name and fall back to the default aisle (P) |
| Grocery ingredient catalog | post a companion `groceryingredients` row | does not | keeps v1 to a single write per commit |
| Aisle for a new pantry item | send the aisle name and uid | the aisle Paprika remembers for that ingredient, otherwise `aisle: ""`, `aisle_uid: ""` | the captures show stored pantry rows with an empty `aisle_uid`; accepted as sent (A 2026-10-09) |
| Pantry `location_uid` of a new item | the location the user picked, by default the default location's uid | `null` | the adapter does not read locations yet. The apps put an item without a location into the default one; a uid they do not know would make them delete the item (P) |
| Login | Paprika 4: `/api/v3/account/login/` with `email`, `password`, `install_uid` and, when there is one, a purchase receipt. Basic auth is the legacy v1 mode of an install upgraded from Paprika 3 (P) | `/api/v1/account/login/`, form body with `email` and `password` only | works (A) and needs no install id. If login fails with correct credentials, moving to v3 is the first thing to try |

Login is the one place where the adapter does not follow the apps. A fresh login in Paprika 4 goes to
`/api/v3/account/login/` and also sends an `install_uid`; `/api/v1/account/login/` is what an install upgraded
from Paprika 3 calls once, to trade its stored password for a token (P). The adapter still logs in on v1: it
works (A), its token is accepted on v2 and v3 (A), and whether v3 takes a login without `install_uid` has not
been tried. It is the first thing to change when the v1 login stops working.

## What the Paprika 4 apps do

Paprika 4 shipped on 2026-11-06 as an update of Paprika 3, not a separate app: the macOS bundle id is still
`com.hindsightlabs.paprika.mac.v3`, the Android package is still `…paprika.android.v3`, and Paprika's help says one
account is used from "any version of Paprika". Owners of Paprika 3 get the Standard tier with cloud sync for free.

This section is what the apps themselves do, noted on 2026-11-12 and 2026-11-14 (evidence level P). Nothing in it
was learned from the server: it shows what a client sends and expects, not what Paprika accepts. Where a point
was also checked on a real account, [Verified against a real account](#verified-against-a-real-account) says so.

| Tag | App |
|---|---|
| **AND** | Paprika for Android 4.0.15 (build 84) |
| **MAC** | Paprika for macOS 4.0.22 (build 70) |

The two apps agree on everything below unless a row says otherwise. Where only one is named, the other was not
checked, not found to differ. AND was looked at in more detail: request paths, parameter names and row keys are
known for both, the behaviour around them (sync order, merging, fallbacks) mostly for AND.

Of all this the adapter uses the login, `sync/status/`, the reads of recipes, categories, meals, meal types,
grocery lists, grocery items, aisles, grocery ingredients, pantry and photos, the writes of meals, grocery items,
pantry items and recipes, and `sync/notify/`; see [Operations](#operations).

### Transport

| Topic | Value | Source |
|---|---|---|
| Hosts | `https://www.paprikaapp.com/` (production), `https://dev.paprikaapp.com/`, a local server on port 4000 | AND, MAC |
| URL | `{host}api/{version}/{path}`; the version is a property of the client, one value for **every** call | AND, MAC |
| Versions | enum with exactly two cases, `v1` and `v3`. Default `v3`. There is no `v2` in either app | AND, MAC |
| Auth, v3 | `Authorization: Bearer {token}`; the token is a JWT whose `sub` is the account uid | AND, MAC |
| Auth, v1 | `Authorization: Basic base64(email:password)`, used only for an install that still stores a Paprika 3 password, until it is upgraded (below) | AND, MAC |
| User-Agent | `Paprika Recipe Manager 4/{app version} ({OS version})`, e.g. `… (Android 15)` | AND |
| Response | always `{"result": …}` or `{"error": {"code": int, "message": string, "owner": string?}}`; an `error` is checked first, also on HTTP 200 | AND, MAC |
| HTTP status | any status >= 400 is an error; 401 means "invalid or expired credentials", 503 "server maintenance" | AND, MAC |
| Compression | responses: gzip/deflate via `Accept-Encoding`. Request payloads: gzip of UTF-8 JSON | AND |
| Timeouts | 200 s per request | AND |
| Methods | `GET` without a body, `POST` with one (or forced), `PUT` and `DELETE` only for `calendar/` | AND |

Error codes known to the client (AND): `1000` activation not found, `1001` activations exceeded, `2000` site
parsing error, `4000` premium required, **`4001` sync not allowed** (the account's plan lapsed; the app stops
pushing and shows "sync is paused"), `4002` subscription belongs to another account (`owner` carries its masked
email), `4005` subscription already active, `5001` trial limit reached, `6000` recipe not found, `6001` cookbook
not found. `0` with message `Unrecognized product.` is also tested for.

### Login

`POST api/{version}/account/login/`

| Field | Notes |
|---|---|
| `email`, `password` | AND lowercases and trims the email first |
| `install_uid` | a per-install id |
| AND: `data`, `signature` | Google Play receipt of a Paprika 3 purchase, when there is one |
| AND: `subscription` | JSON `{"data","signature"}` of a held subscription, when there is one |
| MAC: `app_transaction`, `jws` | App Store equivalents, optional |

Result: `{"token": JWT, "status": JWS}`. `status` is a signed account status (issuer `paprikaapp.com`, claims
`scope` = `account`, `sub`, `mode`, `default_mode`, `mode_exp`) from which the app derives Free / Standard /
Premium.

Encoding: AND posts multipart form fields; MAC most likely a URL-encoded form (`application/x-www-form-urlencoded`),
as its other account calls do.

Which version the login goes to:

- A fresh login in Paprika 4 goes to **`api/v3/account/login/`**, because the client's version defaults to v3.
- An install carrying a Paprika 3 password starts in v1 mode (Basic auth) and immediately calls the same login
  method, which therefore goes to `api/v1/account/login/` with Basic auth and the purchase receipt. The token it
  returns is then used as the v3 bearer token and the stored password is deleted. AND refuses to do this without
  a receipt; the server may not require one.

So `api/v1/account/login/` is the migration path, not the normal one.

Other account calls: `account/register/` (`name`, `email`, `password`, `install_uid`, receipts),
`account/status/` (GET), `account/change_email/` (`email`, `new_email`, `password`; on v3 returns a new `token` and
`status`), `account/change_name/` (`name`), `account/change_password/` (`current_password`, `new_password`),
`account/reset_password/` (`email`), `account/request_deletion/` (`password`).

### Sync

#### Order of one sync run (AND)

`status` → `grocerylists` → `groceryaisles` → `groceries` → `groceryingredients` → `bookmarks` → `cookbooks` →
`recipesearches` → `categories` → `recipes` → `mealtypes` → `meals` → `menus` → `menuitems` → `pantrylocations`
→ `pantry` → `photos` → `notify` (only if this run pushed anything).

#### Requests

| Request | Body | Result |
|---|---|---|
| `GET sync/status/` | | object of collection name → integer revision |
| `GET sync/{collection}/` | | array of every row that is not deleted |
| `POST sync/{collection}/` | multipart, part `data`, filename `file`, `application/octet-stream`: gzip of a JSON **array** of whole rows | |
| `GET sync/recipes/` | | array of `{uid, hash}` |
| `GET sync/recipe/{uid}/` | | one recipe object |
| `POST sync/recipe/{uid}/` | part `data` (filename `file`): gzip of **one** JSON object; optional part `photo_upload` with the thumbnail file when it is not uploaded yet and the recipe is not deleted | |
| `GET sync/photo/{uid}/` | | object with the download link |
| `POST sync/photo/{uid}/` | part `data`: gzip of one photo object; optional part `photo_upload` with the image | |
| `POST sync/notify/` | empty | |

Collections: `grocerylists`, `groceryaisles`, `groceries`, `groceryingredients`, `bookmarks`, `cookbooks`,
`recipesearches`, `categories`, `mealtypes`, `meals`, `menus`, `menuitems`, `pantrylocations`, `pantry`, `photos`
(`photos` is read as a collection but written one photo at a time).

#### Revisions (`sync/status/`)

The values are per-collection revision numbers (AND).

- In a partial (automatic) sync a collection is downloaded only when the stored revision differs from the
  server's. A full sync downloads everything.
- After a download the client stores the server's revision.
- After pushing N rows of a collection the client adds N to its stored revision, without asking the server. The
  server therefore advances a collection's revision by one per row written.

#### Merging a download (AND)

- A server row unknown locally is created. A local row is overwritten only if its status is `unmodified` (or
  `default`); locally modified rows win and are pushed afterwards.
- A local `unmodified` row missing from the server's list is deleted locally: **absence means deleted**.
- Rows are sent with `"deleted": true` to delete them; the client then drops them.
- Recipes: a recipe is fetched when it is new, or unmodified locally and its `hash` in the list differs from the
  stored one. The client stores the server's `hash` as is.

#### `notify` and the websocket

After a run that pushed at least one row to any collection, the client posts `sync/notify/` (AND). Other
devices of the account hold a websocket and receive an `update` event, on which they queue a partial sync.

- Address `ws://www.paprikaapp.com/socket/` (plain `ws`, both apps).
- After connecting the client sends the text frame `{"event":"subscribe","email":"<account email>"}`. No token
  is sent.
- Events received, by the `event` key: `update` (sync now), `family_updated`, `new_message`,
  `subscription_updated`.
- Keep-alive ping every 30 s; reconnect with backoff `5 s × attempt²` up to 600 s, with jitter.
- Without the socket a device still syncs on a 600 s timer and when the app is activated.
- The adapter posts `sync/notify/` but does not connect to the socket. The macOS app's `Info.plist` sets
  `NSAllowsArbitraryLoads`, which is what lets it use plain `ws://`.

#### Identifiers and hashes

| Item | Paprika 4 | Source |
|---|---|---|
| New uid | UUID **version 7**, uppercase | AND, MAC |
| `hash` of a recipe | **random**: 8 random bytes as 16 uppercase hex digits, regenerated on every local change. It is a change token, not a content hash | AND, MAC |
| `photo_hash`, photo `hash` | uppercase SHA-256 of the image bytes; compared case-insensitively | AND |
| Dates | `yyyy-MM-dd HH:mm:ss`, no zone, the device's local time | AND |
| Default cookbook | uid `9e50c366-3397-4c14-86f0-1fd7bb180990` is a client-side constant and is sent as `cookbook_uid: null` | AND |

### Collections Paprika 4 added

They exist only under `/api/v3/sync/`; v2 answers `Not found.` for them (A, 2026-11-12). The adapter reads none
of them yet.

| Collection | What a real account returned (A, 2026-11-12) and what the apps do with it (P) |
|---|---|
| `sync/pantrylocations/` | pantry locations (fridge, freezer, cupboard). Rows `{uid, name, order_flag, is_default}`; the account checked has one, `Pantry`, default, with a lowercase server-style uid. `pantry.location_uid` points here; the apps delete a pantry item whose location they do not know (P). |
| `sync/cookbooks/` | cookbooks. Rows `{uid, name, order_flag, is_default, created, owner_uid, shared_uid, shared_name}`; one default `My Cookbook` on the account checked. `recipes.cookbook_uid` and `categories.cookbook_uid` point here (P; `null` is the default cookbook), but were still `null` on every row on 2026-11-12. |
| `sync/recipesearches/` | saved searches (Standard tier and up); empty on the account checked. The apps write `{uid, name, order_flag, sort_order, rules}` (P) |

`sync/menus/`, `sync/menuitems/` and `sync/bookmarks/` existed in Paprika 3 already and are not implemented here
either.

### Rows as the apps write them

Key sets are identical in AND and MAC. Every row carries `uid` and `deleted`.

| Collection | Keys written (besides `uid`, `deleted`) |
|---|---|
| `groceries` | `name`, `order_flag`, `purchased`, `recipe`, `aisle`, `ingredient`, `instruction`, `quantity`, `separate`, `aisle_uid`, `list_uid` |
| `grocerylists` | `name`, `order_flag`, `is_default`, `reminders_list` |
| `groceryaisles` | `name`, `order_flag` |
| `groceryingredients` | `name`, `aisle_uid` |
| `meals` | `order_flag`, `name`, `date`, `type`, `recipe_uid`, `type_uid`, **`is_ingredient`**, **`scale`** |
| `mealtypes` | `order_flag`, `name`, `color`, `export_all_day`, `export_time`, `original_type` |
| `pantry` | `aisle`, `expiration_date`, `purchase_date`, `has_expiration`, `ingredient`, `in_stock`, `quantity`, **`notes`**, `aisle_uid`, **`location_uid`** |
| `pantrylocations` | `name`, `order_flag`, `is_default` |
| `categories` | `order_flag`, `name`, `parent_uid`, `cookbook_uid` |
| `cookbooks` | `name`, `shared_name`, `order_flag`, `is_default`, `shared_uid`, `owner_uid` (only when owned by someone else), `created` |
| `recipesearches` | `name`, `order_flag`, `sort_order`, `rules` |
| `bookmarks` | `order_flag`, `title`, `url` |
| `menus` | `order_flag`, `name`, `notes`, `days` |
| `menuitems` | `name`, `order_flag`, `day`, `menu_uid`, `recipe_uid`, `type_uid`, `is_ingredient`, `scale` |
| photo (`sync/photo/{uid}/`) | `filename`, `name`, `order_flag`, `recipe_uid`, `hash` |

Recipe (`sync/recipe/{uid}/`): `created`, `hash`, `name`, `description`, `ingredients`, `directions`, `notes`,
`nutritional_info`, `prep_time`, `cook_time`, `total_time`, `difficulty`, `servings`, `rating`, `scale`, `source`,
`source_url`, `on_favorites`, `is_pinned`, `in_trash`, `photo`, `photo_large`, `photo_hash`, `image_url`,
`categories` (uids, sorted), `cookbook_uid`; plus

- `cook_minutes`, `prep_minutes`, `total_minutes`, `servings_min`, `servings_max`, `metadata_version` — sent only
  when `metadata_version` > 0;
- `nutrition` — sent only when the recipe has structured nutrition.

Never written: `photo_url`, `on_grocery_list`.

#### Types of the fields Paprika 4 added to recipes (AND)

| Field | Type | Meaning |
|---|---|---|
| `metadata_version` | integer | version of the derived metadata; current is `2`. A client that reads a lower or missing value recomputes the fields below from the text fields |
| `prep_minutes`, `cook_minutes`, `total_minutes` | integer or null | parsed from `prep_time`, `cook_time`, `total_time` |
| `servings_min`, `servings_max` | number (float) or null | parsed from `servings`; equal when it is not a range |
| `cookbook_uid` | string or null | null is the default cookbook |
| `nutrition` | object or null | `calories`, `total_fat`, `saturated_fat`, `trans_fat`, `polyunsaturated_fat`, `monounsaturated_fat`, `cholesterol`, `sodium`, `total_carbohydrate`, `dietary_fiber`, `total_sugars`, `added_sugars`, `protein`, `vitamin_d`, `calcium`, `iron`, `potassium` (numbers or null), `serving_size` (string), `serving_grams`, `servings` (numbers), `overrides` (string), `version` (integer) |

#### How the apps read a row with missing references (AND)

| Row | Value | The app does |
|---|---|---|
| grocery item, pantry item | `aisle_uid` empty or null | looks the aisle up by the `aisle` name, else uses the default aisle |
| grocery item, pantry item | `aisle_uid` set but unknown | uses the default aisle and pushes the row back |
| grocery item | `list_uid` null | puts it on the default list; an unknown `list_uid` **deletes** the item |
| grocery item | `quantity` and `instruction` both null | re-parses `name` into quantity / ingredient / instruction |
| pantry item | `location_uid` null | puts it in the default location; an unknown one **deletes** the item |
| pantry item | `in_stock` null → true; `has_expiration` null → whether there is an `expiration_date`; `notes` null → `""` | |
| meal | `type_uid` unknown or null | falls back to the built-in type numbered by `type` (0 breakfast, 1 lunch, 2 dinner, 3 snacks), else snacks |
| meal | `recipe_uid` unknown | clears it and pushes the row back; `is_ingredient` null → false |
| meal type | `color` null → `#000000` | |
| category | `parent_uid` unknown or deleted | clears it and pushes the row back |

### Everything else the apps call

Not sync, listed so that a new path in a later version stands out. All under `api/{version}/`.

| Area | Paths |
|---|---|
| Family (Premium) | `family/list/` → `{family: [{uid,email,name,is_account_owner}], invites: [{code,email,created,is_expired}]}`, `family/invite/` (`email`), `family/cancel_invite/` (`code`), `family/resend_invite/` (`code`), `family/remove/` (`uid`) |
| Messages (Premium) | `messages/send/` (`type`, `emails[]`, `recipe_uids[]`, `cookbook_uid`, `text`, `subject`), `messages/list/?before_date=&after_date=&limit=10`, `messages/mark_as_read/` (`start_date`, `end_date`), `messages/{uid}/recipes/`, `messages/{uid}/recipes/{recipe}/`, `messages/{uid}/cookbooks/{shared}/categories/`, `…/recipes/`, `…/recipes/{recipe}/` |
| Calendar feed | `calendar/` GET, POST (`time_zone`), PUT (`time_zone`, `combine_meals`), DELETE → `{url, combine_meals}` |
| Recipe import (server-side) | `recipe/` (`url`, gzip `html`, gzip `styles`), `recipe/parse/` (`text`, `url`), `recipe/scan/` (`file_upload` image), `recipe/suggest_categories/`, MAC also `recipe/save/` and `sites/` |
| Purchases | `purchase/checkout/`, `purchase/upload/`, `purchase/update/`, `purchase/validate/android/`, `subscription/trial/`, `subscription/manage/`, `offers/shown/`, `offers/result/`, `offers/owed/?install_id=`, `products/{code}/`, `license/activate/`, `license/validate/`, `license/deactivate/` |
| Devices | `devices/register/`, `devices/unregister/` (`token`, push notifications) |
| Alexa / OAuth | `alexa/urls/`, `alexa/status/`, `alexa/link/`, `alexa/unlink/`, `alexa/lists/enable/`, `alexa/lists/disable/`, `alexa/lists/status/`, `oauth/authorize/` |
| Statistics | `statistics/` (AND) |

## Verified against a real account

| When | Apps in use | Sync API | What |
|---|---|---|---|
| 2026-10-09, 2026-10-10 | Paprika 3.8.4 | v2 | every read; the three create-only writes, the meal-plan edit, the pantry edit and removal |
| 2026-11-12 | Paprika 4.0.22 | v2 against v3 | every read, read-only |
| 2026-11-14 | Paprika 4.0.22, Paprika 4 for Android | v3 | every read and every write, among them the recipe edit for the first time, and `sync/notify/` |

Still to do: the tool path (propose, approve, commit) has run against the fake only; the integration tests
exercise the client layer underneath it.

### 2026-10-09 and 2026-10-10: reads and writes on v2, Paprika 3.8.4 in use

Reads were checked first from the cache a full sync produced, then with the live integration test
(`TestLiveReads`, passed).

| Write | Result on 2026-10-09 |
|---|---|
| Meal-plan entry (`TestLiveMealWrite`) | passed: one meal by name on 2027-12-31 was accepted, returned by `GET /sync/meals/` with the same name, date and meal type, and gone from it after the soft delete |
| Meal-plan edit (`TestLiveMealEdit`) | passed: an entry the test created itself was moved a day, given another meal type and other text by posting its whole row again under the same uid; Paprika returned the changed row, still one row, with `scale`, `is_ingredient` and `order_flag` as before |
| Meal-plan entry pointing at a recipe (`recipe_uid`) | not run on that day; passed on 2026-11-14, see [2026-11-14](#2026-11-14-writes-on-v3-paprika-4022-in-use) |
| Grocery items (`TestLiveGroceryWrite`) | passed against the list `adapter-test`: one item was accepted, returned by `GET /sync/groceries/` with the same name and list, unpurchased; posting the same uid again left exactly one row; gone after the soft delete |
| Pantry items (`TestLivePantryWrite`) | passed: one item with a quantity and an expiration date was accepted and returned by `GET /sync/pantry/` with the same values, in stock; posting the same uid again left exactly one row; gone after the soft delete. The returned row has the 11 expected fields, which also settles the pantry field set below |
| Pantry edit and removal (`TestLivePantryEdit`, 2026-10-10) | passed: an item the test created itself was renamed, given another quantity, marked out of stock and given an expiration and a purchase date by posting its whole row again under the same uid; Paprika returned the changed row with every other field as before. Posting it with `has_expiration: false`, `expiration_date: null` removed the expiration date. Posting the whole row with `deleted: true` made `GET /sync/pantry/` stop returning it |
| Aisle of a new grocery item | sent `aisle: ""`, `aisle_uid: null`; Paprika stored and returned exactly that, it does not assign an aisle itself. The apps then look the aisle up by its name and fall back to the default aisle (P) |

| Collection | Rows | Result |
|---|---|---|
| Login, change counters, recipe list | | worked: every collection below ended `fresh` with no error |
| Recipes | 157 | 36 fields: the 28 known ones plus 8 new, all present, none missing |
| Categories | 11 | 1 new field (`cookbook_uid`), none missing |
| Meals | 963 | fields as expected |
| Meal types, aisles, grocery ingredients, grocery lists, grocery items | 4, 26, 8, 2, 3 | fields as expected |
| Pantry | 0 | request worked; field set verified later that day through the pantry write test |

New fields found, since added to the adapter's known list:

| Where | Fields | Values seen |
|---|---|---|
| Recipe | `prep_minutes`, `cook_minutes`, `total_minutes`, `servings_min`, `servings_max`, `nutrition`, `cookbook_uid`, `metadata_version` | `null` on all 157 recipes |
| Category | `cookbook_uid` | not inspected |

Every value seen on that day is `null`, so the types come from the apps (P): `prep_minutes`, `cook_minutes` and
`total_minutes` are integers, `servings_min` and `servings_max` numbers, `metadata_version` an integer (currently
`2`), `cookbook_uid` a string, `nutrition` an object of per-nutrient numbers. The apps derive the minutes and
servings from the text fields and send them only once `metadata_version` is set. The adapter stores all of them
and reports them in exports but does not decode or expose them; the older text fields (`prep_time`, `cook_time`,
`total_time`, `servings`, `nutritional_info`) are still what carries the data (26, 38, 47, 103 and 34 recipes
have them filled).

### 2026-11-12: v2 against v3, Paprika 4.0.22 in use

Account in use by Paprika 4.0.22 (macOS). Read-only requests only.

| Check | Result |
|---|---|
| `paprika-adapter verify` (all on v2) | every operation `ok`, no field drift: 39 recipes, 12 categories, 4 meal types, 24 aisles, 1 grocery ingredient, 1 grocery list, 15 grocery items, 0 pantry items, 17 meals, 7 photos |
| `GET /api/v2/sync/status/` | 16 counters, including `pantrylocations`, `cookbooks`, `recipesearches`, `menus`, `menuitems` and `bookmarks` |
| `GET /api/v3/sync/status/` | the same 16 counters |
| `GET /api/v2/sync/{pantrylocations,cookbooks,recipesearches}/` | HTTP 200 with `{"error":{"message":"Not found."}}` |
| `GET /api/v3/sync/{pantrylocations,cookbooks,recipesearches}/` | `result` arrays, shapes under [Collections Paprika 4 added](#collections-paprika-4-added) |
| Every Paprika 3 collection, v2 against v3 | same uids, same field sets, same values; only `photo_url` differs, a signed link minted per request |
| `GET /api/v1/sync/status/`, `/api/v1/sync/pantry/` | `Invalid or expired credentials.` (v1 sync does not take this token) |
| `GET /api/v4/sync/…` | `Not found.` |

Counters are not row counts: `pantrylocations` is 2 with one row, `mealtypes` 52 with four.

### 2026-11-14: writes on v3, Paprika 4.0.22 in use

The integration tests against `/api/v3/sync/`, with the rows in the shape the Paprika 4 apps send.

| Test | Result |
|---|---|
| `TestLiveReads`, `TestLivePhotos` | passed |
| `TestLivePantryWrite` | passed: a new item posted with `notes: null` and `location_uid: null` was accepted and returned with both as `null` |
| `TestLivePantryEdit` | passed on the second run. On the first the removal request timed out after 30 s; the item was gone afterwards all the same |
| `TestLiveMealWrite`, `TestLiveMealEdit` | passed: a new entry posted with `is_ingredient: false` and `scale: null` was accepted and returned with those values. An entry pointing at a recipe (`recipe_uid`) was accepted and returned with it |
| `POST /sync/notify/` without a body | `{"result": true}` |
| `TestLiveGroceryWrite` | passed against a list whose name contains `test`: the item was stored with `aisle: ""`, `aisle_uid: null` as sent, and gone after the soft delete |
| `TestLiveRecipeEdit` | passed against a recipe whose name contains `test`: rating, notes, description and directions were overwritten by posting the whole recipe with a new 16-digit `hash`; Paprika returned the new values and the new hash, every other field as before, and the test then put the old values back the same way |
| Recipe created in Paprika 4.0.22 | its uid is a version 7 UUID and its `hash` 16 hex digits, as the apps were expected to mint them (P) |

Each integration test logs in again. After about fifteen logins within an hour Paprika answered the next one
with HTTP 200 and `{"error":{"message":"Too many attempts. Please wait a few minutes and try again."}}`.

What this means for the adapter:

- Nothing it reads changed. Reading the old collections from v2 or v3 gives the same data.
- `pantrylocations` (and `cookbooks`, `recipesearches`) can only be read from v3, and v3 is the only sync version
  the Paprika 4 apps know, so the adapter moved to v3 as a whole (`paprika.SyncPath`). `verify` against v3 passed on 2026-11-12
  with the same rows and no drift.
- Writes on v3 were checked with the integration tests on 2026-11-14, see above. Whether a pantry row posted
  with a non-null `location_uid` keeps it has not been tried.
- Edits and removals post back the whole row as read, so they keep `location_uid`, `notes` and `cookbook_uid`.
  A new pantry item is sent with `location_uid: null`, so it has no location and the apps show it under the
  default one (P).

## Facts the design depends on

- **Writes are upserts by uid and replace the whole object** (C; A 2026-10-09 for "same uid twice gives one row"). There is no create-vs-update. When creating, the adapter
  therefore only posts uids it minted itself and checks first that Paprika does not already hold them. When
  editing or removing, it posts the row it read from Paprika a moment before, never one from the cache.
- **New uids must be canonical uppercase UUIDs** (C). The Paprika 4 apps mint version 7 ones (P, and A on
  rows created in the apps on 2026-11-14), and so does the adapter since that day; before it minted version 4,
  the same shape, which Paprika accepted as well (A). Server-minted uids come in other shapes (64 hex chars,
  `uuid-number-hex`); the adapter treats every uid as an opaque string and only validates the ones it mints.
- **Errors often arrive as HTTP 200** (L, C). A 200 is success only if the body has `result`.
- **The error body carries a numeric `code`** (L, P). It was `0` on everything seen live. The apps act on a few
  others; the adapter reports `4001` (sync not allowed, the plan lapsed) and `4000` (premium required) as
  `plan_required` and `6000`, `6001` as `not_found`. None of these has been seen from the real server.
- **`status` values are change counters, not row counts** (P; the capture shows `groceries: 1323` beside an
  empty grocery list). The apps call them revisions, download a collection when the server's value differs from
  the one they stored, and add one per row they push. The adapter skips a download when a counter has not moved.
  Because the server's side of this is not verified, every collection is re-downloaded at least every 6 hours
  regardless.
- **Recipe `hash` changes when a recipe changes** (C, T, P). The adapter re-fetches a recipe only when its hash
  differs, and so do the apps.
- **Soft-deleted rows are not returned by `GET`** (A for meals, grocery items and pantry items, 2026-10-09; the
  apps rely on it too and delete locally what a download no longer lists, P). The
  removal of pantry items and the integration test's cleanup depend on it.
- **Token lifetime is unknown** (T). On a 401 the adapter logs in again once and retries.
- **Logins are rate limited** (A 2026-11-14: refused after about fifteen within an hour); limits on other
  requests are unknown (T). The adapter keeps its token, so it logs in rarely. It makes one `status` request per refresh, fetches recipes four at a
  time, backs off after failures, and after Paprika rejects a login waits five minutes before trying another.

## Recipe edits (C; A 2026-11-14)

- The capture of the macOS client shows a recipe saved as a single object posted to the recipe's own URL,
  27 keys: the 28 it reads, minus `photo_url` and `on_grocery_list`, plus `deleted`. A rating change is in
  the capture. The adapter sends the same, plus any further field Paprika returned (the eight found on
  2026-10-09), so that a whole-object replace cannot drop them.
- `hash` is client-owned and, in Paprika 4, a change token rather than a content hash: both apps stamp 64 random
  bits, as 16 uppercase hex digits, on every local change (P). A device fetches a recipe again when the hash in
  the recipe list differs from the one it holds, and stores the server's value as it is. The adapter stamps a
  hash of the same kind on every edit.
- Paprika 3 used the uppercase SHA-256 of a compact JSON array of 27 fields, as `mcp-paprika` derived it from
  Paprika's framework. Until 2026-11-14 the adapter computed that. On the cache of a real account on 2026-10-09
  it reproduced the stored hash of 79 of 157 recipes; the other 78 carried a hash of some other scheme. Recipes
  keep whatever hash they had until they are edited again, so both lengths occur side by side.
- First verified on 2026-11-12 with `TestLiveRecipeEdit` on `/api/v2/` against a throwaway recipe created in
  Paprika 4.0: the edited fields were read back, every other field (times, servings, ingredients, categories, the
  eight newer fields) came back unchanged, and the restore took. The app afterwards showed the recipe with its
  original content, without a sync error.
- Verified with `TestLiveRecipeEdit` on 2026-11-14. On the same day a recipe was edited twice while Paprika
  4.0.22 for macOS and Paprika 4 for Android had it open: first rating and notes, then every text field with the
  matching `*_minutes` and `servings_*` numbers. Both apps showed each change at once and correctly, and neither
  wrote the recipe back: its `hash` was still the adapter's a minute and a half later.
- Opening that recipe in an app's editor and saving without a change makes the app post it again: twice on
  macOS and twice on Android, every one of the 36 fields came back exactly as the adapter had written it, with a
  different `hash` each time, and the `recipes` counter went up by one per save (169 to 172). So the hash is
  not derived from the content, a changed hash does not mean changed content, and the `*_minutes` and
  `servings_*` values the adapter had sent are the ones both apps compute themselves (A).

## Photos (all A, 2026-10-09)

- A recipe with a picture has `photo` (file name of a small cover thumbnail), `photo_hash` and, in the recipe
  detail, `photo_url`. On the account checked, 126 of 157 recipes have one. The reference notes say `photo_url` is
  empty for uploaded photos; here it was set for every recipe with a `photo`.
- `photo_hash` is the SHA-256 of the thumbnail bytes (checked on three recipes). The `hash` of a gallery photo
  is **not** the SHA-256 of what its link serves (checked on one); the adapter does not rely on either.
- Full-resolution photos are separate rows in `photos`, linked by `recipe_uid`: 53 rows across 23 recipes, up
  to 18 for one recipe. A recipe's `photo_large` is the `filename` of the gallery photo that is its cover (21 recipes).
- Download links carry `AWSAccessKeyId`, `Expires` and `Signature`, and expire about 10 minutes after they are
  issued, so the link stored with a cached recipe is useless and a fresh one is requested per download.
- Paprika issues the links with the `http` scheme. The same link works over `https`, which is what the adapter uses.
- The adapter downloads only from `s3.amazonaws.com/uploads.paprikaapp.com/` or from the API host. A link to
  any other host fails with `protocol_changed` and names the host: that is the sign Paprika moved its photo storage.

## Detecting change

Drift is judged against the list `knownFields` in `internal/paprika/types.go`, which is meant to equal what
Paprika returns today. It started as the field set of the 3.8.4 captures; the first real sync
showed Paprika had added fields since. A new field is not a fault, but `sync_status` reports
`healthy: false` until someone has looked at it and added it to the list
([RUNBOOK.md](RUNBOOK.md#protocol-changes)).

- `sync_status` and every tool result report `protocol_drift` when Paprika returns a field the adapter does not
  know, or stops returning one it expects. Unknown fields are kept in the cache and in exports.
- A response that does not fit at all fails with `protocol_changed` and leaves the cache untouched.
- `paprika-adapter verify` reruns the whole read matrix on demand.
