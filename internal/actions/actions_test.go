// Copyright 2024 The Go Authors. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package actions

import (
	"bytes"
	"cmp"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"golang.org/x/oscar/internal/storage"
	"golang.org/x/oscar/internal/testutil"
	"rsc.io/ordered"
)

func TestDB(t *testing.T) {
	var (
		actionKind = "test"
		key        = ordered.Encode("num", 23)
		action     = []byte("action")
		result     = []byte("result")
		error      = errors.New("bad")
	)
	t.Run("before-after", func(t *testing.T) {
		db := storage.MemDB()
		dkey := Before(db, actionKind, key, action, false)
		e, ok := getEntry(db, dkey)
		if !ok {
			t.Fatal("not found")
		}
		want := &Entry{
			Created:          e.Created,
			Kind:             actionKind,
			Key:              key,
			Action:           action,
			ApprovalRequired: false,
			ModTime:          e.ModTime,
		}
		if !reflect.DeepEqual(e, want) {
			t.Errorf("Before:\ngot  %+v\nwant %+v", e, want)
		}

		After(db, dkey, result, error)
		e, ok = getEntry(db, dkey)
		if !ok {
			t.Fatal("not found")
		}
		want.Done = e.Done
		want.ModTime = e.ModTime
		want.Result = result
		want.Error = "bad"
		if !reflect.DeepEqual(e, want) {
			t.Errorf("After:\ngot  %+v\nwant %+v", e, want)
		}
	})
	t.Run("approval", func(t *testing.T) {
		db := storage.MemDB()
		Before(db, actionKind, key, action, true)
		tm := time.Now().Round(0).In(time.UTC)
		d1 := Decision{Name: "name1", Time: tm, Approved: true}
		d2 := Decision{Name: "name2", Time: tm, Approved: false}
		AddDecision(db, actionKind, key, d1)
		AddDecision(db, actionKind, key, d2)
		e, ok := Get(db, actionKind, key)

		if !ok {
			t.Fatal("not found")
		}
		want := &Entry{
			Created:          e.Created,
			ModTime:          e.ModTime,
			Kind:             actionKind,
			Key:              key,
			Action:           action,
			ApprovalRequired: true,
			Decisions:        []Decision{d1, d2},
		}
		if !reflect.DeepEqual(e, want) {
			t.Errorf("\ngot:  %+v\nwant: %+v", e, want)
		}
	})
	t.Run("scan", func(t *testing.T) {
		db := storage.MemDB()
		lg := testutil.Slogger(t)
		var entries []*Entry
		start := time.Now()
		for i := 1; i <= 3; i++ {
			e := &Entry{
				Kind:   fmt.Sprintf("test-%d", i%2),
				Key:    ordered.Encode(i),
				Action: []byte{byte(-i)},
			}
			time.Sleep(50 * time.Millisecond) // ensure each action has a different wall clock time
			Before(db, e.Kind, e.Key, e.Action, false)
			entries = append(entries, e)
		}

		entriesByKey := slices.Clone(entries)
		slices.SortFunc(entriesByKey, func(e1, e2 *Entry) int {
			return cmp.Or(
				cmp.Compare(e1.Kind, e2.Kind),
				bytes.Compare(e1.Key, e2.Key),
			)
		})
		got := slices.Collect(Scan(db, nil, ordered.Encode(ordered.Inf)))
		for i, g := range got {
			if i < len(entriesByKey) {
				entriesByKey[i].Created = g.Created
				entriesByKey[i].ModTime = g.ModTime
			}
		}
		compareSlices(t, got, entriesByKey)

		got = slices.Collect(ScanAfterDBTime(lg, db, 0, nil))
		compareSlices(t, got, entries)

		for _, test := range []struct {
			t    time.Time
			want []*Entry
		}{
			{start, entries},
			{time.Now(), nil},
			{entries[0].Created, entries[1:]},
		} {
			got := slices.Collect(ScanAfter(lg, db, test.t, nil))
			compareSlices(t, got, test.want)
		}
	})
}

func compareSlices[T any](t *testing.T, got, want []T) {
	t.Helper()
	for i := range max(len(got), len(want)) {
		g := "missing"
		w := "missing"
		if i < len(got) {
			g = fmt.Sprintf("%+v", got[i])
		}
		if i < len(want) {
			w = fmt.Sprintf("%+v", want[i])
		}
		if g != w {
			t.Errorf("%d:\ngot  %s\nwant %s", i, g, w)
		}
	}
}

func TestApproved(t *testing.T) {
	approve := Decision{Name: "n", Time: time.Now(), Approved: true}
	deny := Decision{Name: "n", Time: time.Now(), Approved: false}
	for _, test := range []struct {
		req  bool
		ds   []Decision
		want bool
	}{
		{false, nil, true},              // approval not required => approved
		{false, []Decision{deny}, true}, // ...even if there are denials.
		{true, nil, false},
		{true, []Decision{approve}, true},
		{true, []Decision{approve, approve}, true},
		{true, []Decision{approve, deny, approve}, false}, // denials have veto power
	} {
		e := &Entry{
			ApprovalRequired: test.req,
			Decisions:        test.ds,
		}
		if got := e.Approved(); got != test.want {
			t.Errorf("%+v: got %t, want %t", e, got, test.want)
		}
	}
}

// extractUnique extracts the unique value from the key, which is an ordered-encoded
// value of the form [actionKind, k1, k2, ..., u].
// The keyLen argument is the number of intermediate ki's.
func extractUnique(dkey []byte, keyLen int) uint64 {
	args := make([]any, 1+keyLen)
	var u uint64
	args = append(args, &u)
	if err := ordered.Decode(dkey, args...); err != nil {
		panic(err)
	}
	return u
}
