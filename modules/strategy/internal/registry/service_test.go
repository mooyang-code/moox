package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/mooyang-code/moox/modules/strategy/internal/store"
	"github.com/mooyang-code/moox/modules/strategy/schema"
	"gorm.io/gorm"
)

func TestParseManifest(t *testing.T) {
	m, err := Parse("id: demo\nversion: 1.0.0\napi_version: moox.strategy/v1\nentrypoint: strategy.py:run\n")
	if err != nil || m.ID != "demo" {
		t.Fatalf("%+v %v", m, err)
	}
}

func TestParseRejectsUnknownManifestField(t *testing.T) {
	if _, err := Parse("id: demo\nversion: 1.0.0\napi_version: moox.strategy/v1\nentrypoint: strategy.py:run\nunknown: true\n"); err == nil {
		t.Fatal("expected unknown manifest field to be rejected")
	}
}

func TestPublishKeepsVersionImmutable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	r := &Service{Repo: store.New(db)}
	manifest := "id: demo\nversion: 1.0.0\napi_version: moox.strategy/v1\nentrypoint: strategy.py:run\n"
	if _, err := r.Publish(context.Background(), manifest, "def run(a,b,c,d): return {'action':'hold','targets':[],'next_state':{}}\n"); err != nil {
		t.Fatal(err)
	}
	_, err = r.Publish(context.Background(), manifest, "def run(a,b,c,d): return {'action':'rebalance','targets':[],'next_state':{}}\n")
	if !errors.Is(err, ErrImmutableVersion) {
		t.Fatalf("err=%v", err)
	}
}

func TestPublishRejectsManifestChangeWithSameSource(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	r := &Service{Repo: store.New(db)}
	manifest := "id: demo\nversion: 1.0.0\napi_version: moox.strategy/v1\nentrypoint: strategy.py:run\n"
	source := "def run(a,b,c,d): return {'action':'hold','targets':[],'next_state':{}}\n"
	if _, err := r.Publish(context.Background(), manifest, source); err != nil {
		t.Fatal(err)
	}
	changed := manifest + "state_schema_version: 2\n"
	if _, err := r.Publish(context.Background(), changed, source); !errors.Is(err, ErrImmutableVersion) {
		t.Fatalf("err=%v", err)
	}
}

func TestSavePromotesLoadedDraft(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(schema.AllSQL()).Error; err != nil {
		t.Fatal(err)
	}
	r := &Service{Repo: store.New(db)}
	manifest := "id: demo\nversion: 1.0.0\napi_version: moox.strategy/v1\nentrypoint: strategy.py:run\n"
	source := "def run(a,b,c,d): return {'action':'hold','targets':[],'next_state':{}}\n"
	if _, err := r.Publish(context.Background(), manifest, source); err != nil {
		t.Fatal(err)
	}
	d, err := r.Prepare(manifest, source)
	if err != nil {
		t.Fatal(err)
	}
	d.Status = "enabled"
	if err := r.Save(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	got, err := r.Repo.GetDefinition(context.Background(), d.StrategyID, d.Version)
	if err != nil || got.Status != "enabled" {
		t.Fatalf("got=%+v err=%v", got, err)
	}
}
