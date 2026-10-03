package claims_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sheathedsharp/option-berth/internal/store"
)

func observedClaim(port int, key string) store.ClaimRow {
	at := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return store.ClaimRow{Port: port, Key: key, Project: "fixture", Worktree: "main", CreatedAt: at, ExpiresAt: at.Add(time.Hour)}
}

func TestDeleteObservedClaimsRetainsChangedSets(t *testing.T) {
	for _, change := range []string{"refresh", "extra port", "removed", "foreign owner", "metadata", "other key"} {
		t.Run(change, func(t *testing.T) {
			st := openStore(t)
			c := st.Claims()
			a, z := observedClaim(12001, "a"), observedClaim(12002, "z")
			if err := c.Put(a, z); err != nil {
				t.Fatal(err)
			}
			observed, err := c.ObserveContext(context.Background(), "a", "z")
			if err != nil {
				t.Fatal(err)
			}
			other, err := store.Open(st.DBPath())
			if err != nil {
				t.Fatal(err)
			}
			defer other.Close()
			writer := other.Claims()
			switch change {
			case "refresh":
				a.ExpiresAt = a.ExpiresAt.Add(time.Second)
				err = writer.Put(a)
			case "extra port":
				err = writer.Put(observedClaim(12003, "a"))
			case "removed":
				_, err = writer.Delete("a")
			case "foreign owner":
				if _, err = writer.Delete("a"); err == nil {
					a.Key = "replacement"
					err = writer.Put(a)
				}
			case "metadata":
				a.Project = "replacement"
				err = writer.Put(a)
			case "other key":
				z.ExpiresAt = z.ExpiresAt.Add(time.Second)
				err = writer.Put(z)
			}
			if err != nil {
				t.Fatal(err)
			}
			before, err := c.List()
			if err != nil {
				t.Fatal(err)
			}
			n, err := c.DeleteObservedContext(context.Background(), observed)
			if n != 0 || !errors.Is(err, store.ErrClaimsChanged) {
				t.Fatalf("delete = %d, %v", n, err)
			}
			after, err := c.List()
			if err != nil {
				t.Fatal(err)
			}
			if len(after) != len(before) {
				t.Fatalf("changed set was partly deleted: before=%+v after=%+v", before, after)
			}
			for i := range before {
				if before[i] != after[i] {
					t.Fatalf("row changed: %+v -> %+v", before[i], after[i])
				}
			}
		})
	}
}

func TestDeleteObservedClaimsDoesNotSweepOtherExpiredKeys(t *testing.T) {
	c := openStore(t).Claims()
	a, b, other := observedClaim(12101, "a"), observedClaim(12102, "a"), observedClaim(12103, "unrelated-expired")
	if err := c.Put(a, b, other); err != nil {
		t.Fatal(err)
	}
	observed, err := c.ObserveContext(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	n, err := c.DeleteObservedContext(context.Background(), observed)
	if err != nil || n != 2 {
		t.Fatalf("delete = %d, %v", n, err)
	}
	rows, err := c.List()
	if err != nil || len(rows) != 1 || rows[0].Key != other.Key {
		t.Fatalf("unrelated expiry changed: %+v, %v", rows, err)
	}
}

func TestDeleteObservedClaimsRollsBackPartialDatabaseFailure(t *testing.T) {
	st := openStore(t)
	c := st.Claims()
	if err := c.Put(observedClaim(12201, "a"), observedClaim(12202, "z")); err != nil {
		t.Fatal(err)
	}
	observed, err := c.ObserveContext(context.Background(), "a", "z")
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB().Exec(`CREATE TRIGGER deny_second_release BEFORE DELETE ON claims WHEN OLD.key = 'z' BEGIN SELECT RAISE(ABORT, 'fixture refusal'); END`)
	if err != nil {
		t.Fatal(err)
	}
	n, err := c.DeleteObservedContext(context.Background(), observed)
	if err == nil || n != 0 {
		t.Fatalf("delete = %d, %v", n, err)
	}
	rows, err := c.List()
	if err != nil || len(rows) != 2 {
		t.Fatalf("earlier key was not rolled back: %+v, %v", rows, err)
	}
}

func TestDeleteObservedClaimsRejectsCanceledOrInvalidEvidence(t *testing.T) {
	for _, kind := range []string{"cancel", "zero created", "zero expiry", "duplicate"} {
		t.Run(kind, func(t *testing.T) {
			c := openStore(t).Claims()
			row := observedClaim(12301, "a")
			if err := c.Put(row); err != nil {
				t.Fatal(err)
			}
			observed, err := c.ObserveContext(context.Background(), "a")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch kind {
			case "cancel":
				cancel()
			case "zero created":
				observed.Rows[0].CreatedAt = time.Time{}
			case "zero expiry":
				observed.Rows[0].ExpiresAt = time.Time{}
			case "duplicate":
				observed.Rows = append(observed.Rows, row)
			}
			if n, err := c.DeleteObservedContext(ctx, observed); n != 0 || err == nil {
				t.Fatalf("delete = %d, %v", n, err)
			}
			rows, err := c.Get("a")
			if err != nil || len(rows) != 1 {
				t.Fatalf("lost reservation: %+v, %v", rows, err)
			}
		})
	}
}

func TestDeleteObservedClaimsSeesAnotherStoreRefresh(t *testing.T) {
	st := openStore(t)
	c := st.Claims()
	row := observedClaim(12401, "a")
	if err := c.Put(row); err != nil {
		t.Fatal(err)
	}
	observed, err := c.ObserveContext(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	other, err := store.Open(st.DBPath())
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	row.ExpiresAt = row.ExpiresAt.Add(time.Minute)
	if err := other.Claims().Put(row); err != nil {
		t.Fatal(err)
	}
	if n, err := c.DeleteObservedContext(context.Background(), observed); n != 0 || !errors.Is(err, store.ErrClaimsChanged) {
		t.Fatalf("delete = %d, %v", n, err)
	}
}

func TestDeleteObservedClaimsRejectsIdenticalRefreshAndRecreation(t *testing.T) {
	for _, kind := range []string{"identical refresh", "identical recreation"} {
		t.Run(kind, func(t *testing.T) {
			c := openStore(t).Claims()
			row := observedClaim(12451, "a")
			if err := c.Put(row); err != nil {
				t.Fatal(err)
			}
			observed, err := c.ObserveContext(context.Background(), "a")
			if err != nil {
				t.Fatal(err)
			}
			if kind == "identical recreation" {
				if _, err := c.Delete("a"); err != nil {
					t.Fatal(err)
				}
			}
			if err := c.Put(row); err != nil {
				t.Fatal(err)
			}
			if n, err := c.DeleteObservedContext(context.Background(), observed); n != 0 || !errors.Is(err, store.ErrClaimsChanged) {
				t.Fatalf("delete = %d, %v", n, err)
			}
		})
	}
}
