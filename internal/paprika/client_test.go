package paprika_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/pavel-alay/paprika-adapter/internal/paprika"
	"github.com/pavel-alay/paprika-adapter/internal/paprikatest"
)

var day = time.Date(2026, 10, 8, 12, 0, 0, 0, time.UTC)

func newClient(t *testing.T, fake *paprikatest.Server, mutate ...func(*paprika.Options)) *paprika.Client {
	t.Helper()
	opts := paprika.Options{BaseURL: fake.URL, Email: paprikatest.Email, Password: paprikatest.Password, RetryWait: time.Millisecond, HTTPClient: fake.Client()}
	for _, m := range mutate {
		m(&opts)
	}
	c, err := paprika.New(opts)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return c
}

func newFake(t *testing.T) *paprikatest.Server {
	t.Helper()
	fake := paprikatest.New(day)
	t.Cleanup(fake.Close)
	return fake
}

func wantKind(t *testing.T, err error, kind paprika.Kind) {
	t.Helper()
	if got := paprika.KindOf(err); got != kind {
		t.Fatalf("error kind = %q (%v), want %q", got, err, kind)
	}
}

func TestLoginOnceThenReuseToken(t *testing.T) {
	fake := newFake(t)
	c := newClient(t, fake)
	for range 3 {
		rows, err := c.List(context.Background(), paprika.Meals)
		if err != nil {
			t.Fatalf("list meals: %v", err)
		}
		if len(rows) != 5 {
			t.Fatalf("meals = %d, want 5", len(rows))
		}
	}
	if n := fake.Hits("POST /api/v1/account/login/"); n != 1 {
		t.Fatalf("logins = %d, want 1", n)
	}
}

func TestTokenFileIsPrivateAndReusedAcrossClients(t *testing.T) {
	fake := newFake(t)
	path := filepath.Join(t.TempDir(), "token")
	withFile := func(o *paprika.Options) { o.TokenFile = path }
	if _, err := newClient(t, fake, withFile).Status(context.Background()); err != nil {
		t.Fatalf("status: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("token file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode = %v, want 0600", info.Mode().Perm())
	}
	if _, err := newClient(t, fake, withFile).Status(context.Background()); err != nil {
		t.Fatalf("status with cached token: %v", err)
	}
	if n := fake.Hits("POST /api/v1/account/login/"); n != 1 {
		t.Fatalf("logins = %d, want 1 (second client must reuse the token file)", n)
	}
}

func TestExpiredTokenTriggersOneRelogin(t *testing.T) {
	fake := newFake(t)
	c := newClient(t, fake)
	if _, err := c.Status(context.Background()); err != nil {
		t.Fatalf("status: %v", err)
	}
	fake.RevokeToken()
	if _, err := c.List(context.Background(), paprika.Pantry); err != nil {
		t.Fatalf("list after token revocation: %v", err)
	}
	if n := fake.Hits("POST /api/v1/account/login/"); n != 2 {
		t.Fatalf("logins = %d, want 2", n)
	}
}

func TestBadCredentialsAreAuthFailureAndNotHammered(t *testing.T) {
	fake := newFake(t)
	c := newClient(t, fake, func(o *paprika.Options) { o.Password = "wrong" })
	for range 3 {
		_, err := c.Status(context.Background())
		wantKind(t, err, paprika.KindAuth)
	}
	if n := fake.Hits("POST /api/v1/account/login/"); n != 1 {
		t.Fatalf("logins = %d, want 1 (rejected credentials must not be retried immediately)", n)
	}
}

func TestErrorSentAsHTTP200IsAnError(t *testing.T) {
	fake := newFake(t)
	_, err := newClient(t, fake).Recipe(context.Background(), "NO-SUCH-RECIPE")
	wantKind(t, err, paprika.KindNotFound)
}

func TestLapsedPlanIsItsOwnErrorAndKeepsPaprikasCode(t *testing.T) {
	fake := newFake(t)
	c := newClient(t, fake)
	fake.SetPlanLapsed(true)
	if _, err := c.List(context.Background(), paprika.Groceries); err != nil {
		t.Fatalf("reading with a lapsed plan: %v", err)
	}
	err := c.AddGroceryItems(context.Background(), []paprika.NewGroceryItem{{UID: paprika.NewUID(), ListUID: "L", Name: "x"}})
	wantKind(t, err, paprika.KindPlan)
	var pe *paprika.Error
	if !errors.As(err, &pe) || pe.Code != 4001 || pe.Retryable() {
		t.Fatalf("error = %+v, want Paprika code 4001 and not retryable", pe)
	}
	if n := fake.Hits("POST /api/v1/account/login/"); n != 1 {
		t.Fatalf("logins = %d, want 1 (a plan refusal is not a token problem)", n)
	}
}

func TestPlanRefusalSentAs401IsNotAnAuthFailure(t *testing.T) {
	fake := newFake(t)
	c := newClient(t, fake)
	fake.SetPlanLapsedWithStatus(http.StatusUnauthorized)
	err := c.AddGroceryItems(context.Background(), []paprika.NewGroceryItem{{UID: paprika.NewUID(), ListUID: "L", Name: "x"}})
	wantKind(t, err, paprika.KindPlan)
	if n := fake.Hits("POST /api/v1/account/login/"); n != 1 {
		t.Fatalf("logins = %d, want 1 (a plan refusal must not drop the token)", n)
	}
}

func TestReadsRetryTransientFailures(t *testing.T) {
	fake := newFake(t)
	fake.FailNext("GET "+paprika.SyncPath+"meals/", 503, 429)
	if _, err := newClient(t, fake).List(context.Background(), paprika.Meals); err != nil {
		t.Fatalf("list: %v", err)
	}
	if n := fake.Hits("GET " + paprika.SyncPath + "meals/"); n != 3 {
		t.Fatalf("attempts = %d, want 3", n)
	}
}

func TestReadsGiveUpAsUnavailable(t *testing.T) {
	fake := newFake(t)
	fake.SetDown(true)
	_, err := newClient(t, fake).Status(context.Background())
	wantKind(t, err, paprika.KindUnavailable)
}

func TestWritesAreSentOnce(t *testing.T) {
	fake := newFake(t)
	fake.FailNext("POST "+paprika.SyncPath+"groceries/", 503)
	err := newClient(t, fake).AddGroceryItems(context.Background(), []paprika.NewGroceryItem{{UID: paprika.NewUID(), ListUID: "L", Name: "x"}})
	wantKind(t, err, paprika.KindUnavailable)
	if n := fake.Hits("POST " + paprika.SyncPath + "groceries/"); n != 1 {
		t.Fatalf("POST attempts = %d, want 1", n)
	}
}

func TestWriteUsesGzippedMultipartArray(t *testing.T) {
	fake := newFake(t)
	uid := paprika.NewUID()
	aisle := "AISLE"
	err := newClient(t, fake).AddGroceryItems(context.Background(), []paprika.NewGroceryItem{{
		UID: uid, ListUID: "LIST", Name: "2 limes", Ingredient: "limes", Quantity: "2", AisleUID: &aisle, OrderFlag: 7,
	}})
	if err != nil {
		t.Fatalf("add: %v", err) // the fake only accepts multipart "data" holding a gzipped JSON array
	}
	for _, r := range fake.Rows("groceries") {
		if r["uid"] != uid {
			continue
		}
		want := map[string]any{"list_uid": "LIST", "name": "2 limes", "ingredient": "limes", "quantity": "2",
			"aisle_uid": "AISLE", "order_flag": float64(7), "purchased": false, "separate": false, "recipe": nil, "instruction": ""}
		for k, v := range want {
			if r[k] != v {
				t.Errorf("stored %s = %#v, want %#v", k, r[k], v)
			}
		}
		return
	}
	t.Fatal("item not stored by the server")
}

// The key sets are the ones the Paprika 4 apps post, see docs/PROTOCOL.md.
func TestNewRowsCarryTheKeysPaprika4Sends(t *testing.T) {
	fake := newFake(t)
	c := newClient(t, fake)
	ctx := context.Background()
	if err := c.AddMeal(ctx, paprika.NewMeal{UID: paprika.NewUID(), Name: "Takeaway", Date: "2026-10-08 00:00:00"}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddPantryItems(ctx, []paprika.NewPantryItem{{UID: paprika.NewUID(), Ingredient: "rice", InStock: true}}); err != nil {
		t.Fatal(err)
	}
	if err := c.AddGroceryItems(ctx, []paprika.NewGroceryItem{{UID: paprika.NewUID(), ListUID: "L", Name: "x"}}); err != nil {
		t.Fatal(err)
	}
	for collection, want := range map[string]string{
		"meals":     "date deleted is_ingredient name order_flag recipe_uid scale type type_uid uid",
		"pantry":    "aisle aisle_uid deleted expiration_date has_expiration in_stock ingredient location_uid notes purchase_date quantity uid",
		"groceries": "aisle aisle_uid deleted ingredient instruction list_uid name order_flag purchased quantity recipe separate uid",
	} {
		sent := fake.Posted(collection)
		if len(sent) != 1 {
			t.Fatalf("%s: %d rows posted, want 1", collection, len(sent))
		}
		keys := make([]string, 0, len(sent[0]))
		for k := range sent[0] {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		if got := strings.Join(keys, " "); got != want {
			t.Errorf("%s keys:\n got %s\nwant %s", collection, got, want)
		}
	}
	meal, pantry := fake.Posted("meals")[0], fake.Posted("pantry")[0]
	if meal["is_ingredient"] != false || meal["scale"] != nil || pantry["notes"] != nil || pantry["location_uid"] != nil {
		t.Errorf("defaults: meal=%v pantry=%v", meal, pantry)
	}
}

func TestMalformedUIDIsRefusedLocally(t *testing.T) {
	fake := newFake(t)
	c := newClient(t, fake)
	for _, uid := range []string{"", "abc", strings.ToLower(paprika.NewUID()), "recipe_" + paprika.NewUID()} {
		wantKind(t, c.AddMeal(context.Background(), paprika.NewMeal{UID: uid, Name: "x"}), paprika.KindInvalid)
	}
	if n := fake.Hits("POST "); n != 0 {
		t.Fatalf("requests sent = %d, want 0", n)
	}
}

func TestUnexpectedResponseShapeIsProtocolError(t *testing.T) {
	for name, body := range map[string]string{
		"not json":           "<html>maintenance</html>",
		"no result or error": `{"data": []}`,
		"rows without uid":   `{"result": [{"name": "x"}]}`,
		"result not a list":  `{"result": {"uid": "x"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			hc := paprikatest.HandlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/login/") {
					_, _ = w.Write([]byte(`{"result":{"token":"t"}}`))
					return
				}
				_, _ = w.Write([]byte(body))
			}))
			c, err := paprika.New(paprika.Options{BaseURL: "https://paprika.test", Email: "a", Password: "b", RetryWait: time.Millisecond, HTTPClient: hc})
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.List(context.Background(), paprika.Meals)
			wantKind(t, err, paprika.KindProtocol)
		})
	}
}

// loginAnswering is a client whose every login gets body; it counts the logins.
func loginAnswering(t *testing.T, body string) (*paprika.Client, *int) {
	t.Helper()
	logins := 0
	hc := paprikatest.HandlerClient(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/login/") {
			logins++
		}
		_, _ = w.Write([]byte(body))
	}))
	c, err := paprika.New(paprika.Options{BaseURL: "https://paprika.test", Email: "a", Password: "b", RetryWait: time.Millisecond, HTTPClient: hc})
	if err != nil {
		t.Fatal(err)
	}
	return c, &logins
}

func TestErrorCodeOfAnotherShapeKeepsTheError(t *testing.T) {
	c, _ := loginAnswering(t, `{"error":{"code":"bad_login","message":"Invalid password."}}`)
	_, err := c.Status(context.Background())
	wantKind(t, err, paprika.KindAuth)
	if !strings.Contains(err.Error(), "Invalid password.") {
		t.Fatalf("error = %v, want Paprika's message", err)
	}
	for _, code := range []string{`"4001"`, `4001.0`, `4.001e3`} {
		c, _ = loginAnswering(t, `{"error":{"code":`+code+`,"message":"Sync is not available."}}`)
		_, err = c.Status(context.Background())
		wantKind(t, err, paprika.KindPlan)
	}
}

func TestPlanRefusedLoginIsNotHammered(t *testing.T) {
	c, logins := loginAnswering(t, `{"error":{"code":4000,"message":"Premium required."}}`)
	for range 3 {
		_, err := c.Status(context.Background())
		wantKind(t, err, paprika.KindPlan)
	}
	if *logins != 1 {
		t.Fatalf("logins = %d, want 1", *logins)
	}
}

func TestCredentialsOnlyTravelOverTLSOrLoopback(t *testing.T) {
	for url, ok := range map[string]bool{
		"https://www.paprikaapp.com": true, "http://127.0.0.1:8080": true, "http://localhost:1": true,
		"http://www.paprikaapp.com": false, "http://10.0.0.5": false, "ftp://x": false, "nonsense": false,
	} {
		_, err := paprika.New(paprika.Options{BaseURL: url, Email: "a", Password: "b"})
		if (err == nil) != ok {
			t.Errorf("base URL %s: err = %v, want ok = %v", url, err, ok)
		}
	}
}

func TestSecretsNeverReachLogsOrErrors(t *testing.T) {
	fake := newFake(t)
	var logs bytes.Buffer
	c := newClient(t, fake, func(o *paprika.Options) {
		o.Logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	})
	ctx := context.Background()
	var errs []string
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err.Error())
		}
	}
	_, err := c.Status(ctx)
	collect(err)
	_, err = c.Recipe(ctx, "NOPE")
	collect(err)
	fake.RevokeToken()
	_, err = c.List(ctx, paprika.Meals)
	collect(err)
	fake.SetDown(true)
	_, err = c.List(ctx, paprika.Meals)
	collect(err)
	collect(c.AddMeal(ctx, paprika.NewMeal{UID: paprika.NewUID(), Name: "x"}))
	collect(c.Login(ctx))

	all := logs.String() + strings.Join(errs, "\n")
	if logs.Len() == 0 || len(errs) < 3 {
		t.Fatalf("test did not exercise logging and errors (logs=%d bytes, errors=%d)", logs.Len(), len(errs))
	}
	for _, secret := range []string{paprikatest.Password, "fixture-token", "Bearer"} {
		if strings.Contains(all, secret) {
			t.Errorf("%q leaked into logs or errors", secret)
		}
	}
}

func TestDetectDrift(t *testing.T) {
	rows := []paprika.Raw{
		{UID: "1", JSON: []byte(`{"uid":"1","name":"A","order_flag":1,"cookbook_uid":null,"brand_new":true}`)},
		{UID: "2", JSON: []byte(`{"uid":"2","name":"B","order_flag":2}`)},
	}
	d := paprika.DetectDrift(string(paprika.GroceryAisles), rows)
	if len(d.Unknown) != 2 || d.Unknown[0] != "brand_new" || d.Unknown[1] != "cookbook_uid" || len(d.Missing) != 0 {
		t.Fatalf("drift = %+v, want unknown=[brand_new cookbook_uid]", d)
	}
	// A field counts as present when any row carries it, even as null.
	d = paprika.DetectDrift(string(paprika.Categories), rows)
	if len(d.Missing) != 1 || d.Missing[0] != "parent_uid" || len(d.Unknown) != 1 || d.Unknown[0] != "brand_new" {
		t.Fatalf("drift = %+v, want missing=[parent_uid] unknown=[brand_new]", d)
	}
	if !paprika.DetectDrift(string(paprika.Categories), nil).Empty() {
		t.Fatal("no rows must mean no drift")
	}
}
