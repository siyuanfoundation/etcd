// Copyright 2015 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package wal

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zaptest"

	"go.etcd.io/etcd/client/pkg/v3/fileutil"
	"go.etcd.io/etcd/server/v3/storage/wal/walpb"
	"go.etcd.io/raft/v3/raftpb"
)

type corruptFunc func(string, int64) error

// TestRepairTruncate ensures a truncated file can be repaired
func TestRepairTruncate(t *testing.T) {
	corruptf := func(p string, offset int64) error {
		f, err := openLast(zaptest.NewLogger(t), p)
		if err != nil {
			return err
		}
		defer f.Close()
		return f.Truncate(offset - 4)
	}

	testRepair(t, makeEnts(10), corruptf, 9)
}

func testRepair(t *testing.T, ents [][]raftpb.Entry, corrupt corruptFunc, expectedEnts int) {
	lg := zaptest.NewLogger(t)
	p := t.TempDir()

	// create WAL
	w, err := Create(lg, p, nil)
	defer func() {
		// The Close might fail.
		_ = w.Close()
	}()
	require.NoError(t, err)

	for _, es := range ents {
		require.NoError(t, w.Save(raftpb.HardState{}, es))
	}

	offset, err := w.tail().Seek(0, io.SeekCurrent)
	require.NoError(t, err)
	require.NoError(t, w.Close())

	require.NoError(t, corrupt(p, offset))

	// verify we broke the wal
	w, err = Open(zaptest.NewLogger(t), p, walpb.Snapshot{})
	require.NoError(t, err)

	_, _, _, err = w.ReadAll()
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.NoError(t, w.Close())

	// repair the wal
	require.True(t, Repair(lg, p))

	// verify the broken wal has correct permissions
	bf := filepath.Join(p, filepath.Base(w.tail().Name())+".broken")
	fi, err := os.Stat(bf)
	require.NoError(t, err)
	expectedPerms := fmt.Sprintf("%o", os.FileMode(fileutil.PrivateFileMode))
	actualPerms := fmt.Sprintf("%o", fi.Mode().Perm())
	require.Equalf(t, expectedPerms, actualPerms, "unexpected file permissions on .broken wal")

	// read it back
	w, err = Open(lg, p, walpb.Snapshot{})
	require.NoError(t, err)

	_, _, walEnts, err := w.ReadAll()
	require.NoError(t, err)
	assert.Len(t, walEnts, expectedEnts)

	// write some more entries to repaired log
	for i := 1; i <= 10; i++ {
		es := []raftpb.Entry{{Index: uint64(expectedEnts + i)}}
		require.NoError(t, w.Save(raftpb.HardState{}, es))
	}
	require.NoError(t, w.Close())

	// read back entries following repair, ensure it's all there
	w, err = Open(lg, p, walpb.Snapshot{})
	require.NoError(t, err)
	_, _, walEnts, err = w.ReadAll()
	require.NoError(t, err)
	assert.Len(t, walEnts, expectedEnts+10)
}

func makeEnts(ents int) (ret [][]raftpb.Entry) {
	for i := 1; i <= ents; i++ {
		ret = append(ret, []raftpb.Entry{{Index: uint64(i)}})
	}
	return ret
}

// TestRepairWriteTearLast repairs the WAL in case the last record is a torn write
// that straddled two sectors.
func TestRepairWriteTearLast(t *testing.T) {
	corruptf := func(p string, offset int64) error {
		f, err := openLast(zaptest.NewLogger(t), p)
		if err != nil {
			return err
		}
		defer f.Close()
		// 512 bytes perfectly aligns the last record, so use 1024
		if offset < 1024 {
			return fmt.Errorf("got offset %d, expected >1024", offset)
		}
		if terr := f.Truncate(1024); terr != nil {
			return terr
		}
		return f.Truncate(offset)
	}
	testRepair(t, makeEnts(50), corruptf, 40)
}

// TestRepairWriteTearMiddle repairs the WAL when there is write tearing
// in the middle of a record.
func TestRepairWriteTearMiddle(t *testing.T) {
	corruptf := func(p string, offset int64) error {
		f, err := openLast(zaptest.NewLogger(t), p)
		if err != nil {
			return err
		}
		defer f.Close()
		// corrupt middle of 2nd record
		_, werr := f.WriteAt(make([]byte, 512), 4096+512)
		return werr
	}
	ents := makeEnts(5)
	// 4096 bytes of data so a middle sector is easy to corrupt
	dat := make([]byte, 4096)
	for i := range dat {
		dat[i] = byte(i)
	}
	for i := range ents {
		ents[i][0].Data = dat
	}
	testRepair(t, ents, corruptf, 1)
}

func TestRepairFailDeleteDir(t *testing.T) {
	p := t.TempDir()

	w, err := Create(zaptest.NewLogger(t), p, nil)
	if err != nil {
		t.Fatal(err)
	}

	oldSegmentSizeBytes := SegmentSizeBytes
	SegmentSizeBytes = 64
	defer func() {
		SegmentSizeBytes = oldSegmentSizeBytes
	}()
	for _, es := range makeEnts(50) {
		if err = w.Save(raftpb.HardState{}, es); err != nil {
			t.Fatal(err)
		}
	}

	_, serr := w.tail().Seek(0, io.SeekCurrent)
	if serr != nil {
		t.Fatal(serr)
	}
	w.Close()

	f, err := openLast(zaptest.NewLogger(t), p)
	if err != nil {
		t.Fatal(err)
	}
	if terr := f.Truncate(20); terr != nil {
		t.Fatal(err)
	}
	f.Close()

	w, err = Open(zaptest.NewLogger(t), p, walpb.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, err = w.ReadAll()
	require.ErrorIsf(t, err, io.ErrUnexpectedEOF, "err = %v, want error %v", err, io.ErrUnexpectedEOF)
	w.Close()

	os.RemoveAll(p)
	require.Falsef(t, Repair(zaptest.NewLogger(t), p), "expect 'Repair' fail on unexpected directory deletion")
}
