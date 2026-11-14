package mcpserver

import (
	"context"
	"sort"
	"strings"

	"github.com/pavel-alay/paprika-adapter/internal/paprika"
	"github.com/pavel-alay/paprika-adapter/internal/store"
)

// pantryPlaces are the pantry locations of Paprika 4 (fridge, freezer, ...), read from the cache.
// An account that has none yields an empty set, and items then show no location.
type pantryPlaces struct {
	all   []paprika.PantryLocation // sorted as in the app
	byUID map[string]paprika.PantryLocation
	def   *paprika.PantryLocation
}

func (s *server) pantryPlaces(ctx context.Context) (pantryPlaces, error) {
	locs, err := store.Load[paprika.PantryLocation](ctx, s.Store, paprika.PantryLocations)
	if err != nil {
		return pantryPlaces{}, err
	}
	sort.SliceStable(locs, func(i, j int) bool { return locs[i].OrderFlag < locs[j].OrderFlag })
	pl := pantryPlaces{all: locs, byUID: make(map[string]paprika.PantryLocation, len(locs))}
	for i, l := range locs {
		pl.byUID[l.UID] = l
		if l.IsDefault && pl.def == nil {
			pl.def = &locs[i]
		}
	}
	return pl, nil
}

// name is the location an item with this location_uid is shown under: no uid means the default location.
func (pl pantryPlaces) name(uid string) string {
	if uid == "" {
		if pl.def != nil {
			return pl.def.Name
		}
		return ""
	}
	return pl.byUID[uid].Name
}

// resolve finds a location by name, ignoring case and surrounding space. Locations
// are created in the Paprika app, never by the adapter.
func (pl pantryPlaces) resolve(field, name string) (paprika.PantryLocation, *ToolError) {
	want := strings.ToLower(strings.TrimSpace(name))
	for _, l := range pl.all {
		if strings.ToLower(strings.TrimSpace(l.Name)) == want {
			return l, nil
		}
	}
	if len(pl.all) == 0 {
		return paprika.PantryLocation{}, invalid(field + ": this Paprika account has no pantry locations")
	}
	return paprika.PantryLocation{}, invalid(field + ": no pantry location named " + quote(name) + "; known locations: " + strings.Join(pl.names(), ", ") +
		". New locations are created in the Paprika app")
}

func (pl pantryPlaces) names() []string {
	out := make([]string, 0, len(pl.all))
	for _, l := range pl.all {
		out = append(out, l.Name)
	}
	return out
}

func (pl pantryPlaces) refs() []NamedRef {
	out := make([]NamedRef, 0, len(pl.all))
	for _, l := range pl.all {
		out = append(out, NamedRef{UID: l.UID, Name: l.Name})
	}
	return out
}

// label fills in the location names of entries.
func (pl pantryPlaces) label(entries []PantryEntry) {
	for i := range entries {
		entries[i].Location = pl.name(entries[i].LocationUID)
	}
}

// labelPantry names the locations of entries from the cache; without locations it leaves them blank.
func (s *server) labelPantry(ctx context.Context, entries ...[]PantryEntry) {
	pl, err := s.pantryPlaces(ctx)
	if err != nil {
		return
	}
	for _, e := range entries {
		pl.label(e)
	}
}

func quote(v string) string { return "\"" + v + "\"" }
