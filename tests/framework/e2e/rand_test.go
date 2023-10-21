package e2e

import (
	"math/rand"
	"testing"

	"go.etcd.io/etcd/server/v3/embed"
)

type Foo struct {
	Str                  string
	IntFixed             int
	Int                  int  `randomize:"randomNumber:10,15"`
	Uint                 uint `randomize:"randomElement:2,4,7,21"`
	Bool                 bool `randomize:"randomBool:"`
	BoolFixed            bool
	ExperimentalBool     bool
	ExperimentalBoolSkip bool `randomize:"skip"`
}

func TestRandStruct(t *testing.T) {
	r := Randomizer{Rand: rand.New(rand.NewSource(1))}

	f := Foo{IntFixed: 123}
	pf := &f
	r.Struct(pf)
	f1 := Foo{
		Int:              15,
		IntFixed:         123,
		Uint:             21,
		Bool:             true,
		ExperimentalBool: true,
	}
	if f != f1 {
		t.Errorf("got %v, wanted %v\n", f, f1)
	}

	r.Struct(pf)
	f2 := Foo{
		Int:              11,
		IntFixed:         123,
		Uint:             7,
		Bool:             true,
		ExperimentalBool: false,
	}
	if f != f2 {
		t.Errorf("got %v, wanted %v\n", f, f2)
	}

	r.Struct(pf)
	f3 := Foo{
		Int:              14,
		IntFixed:         123,
		Uint:             2,
		Bool:             false,
		ExperimentalBool: true,
	}
	if f != f3 {
		t.Errorf("got %v, wanted %v\n", f, f3)
	}

	cfg := embed.NewConfig()
	err := r.Struct(cfg)
	if err != nil {
		t.Fatalf(err.Error())
	}
	expectedSnapshotCount := 138
	if cfg.SnapshotCount != uint64(expectedSnapshotCount) {
		t.Errorf("got SnapshotCount %v, wanted %v\n", cfg.SnapshotCount, expectedSnapshotCount)
	}
}
