/*
Copyright 2024 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package badger

import (
	"bytes"
	"fmt"
	bolt "go.etcd.io/bbolt"
	"io"
	"math"
	"os"
	"strings"
	"sync"

	"github.com/dgraph-io/badger"
	"github.com/prometheus/client_golang/prometheus"

	"go.uber.org/zap"

	"go.etcd.io/etcd/server/v3/interfaces"
)

const dbName = "db"

type DbOpts struct {
}

func Open(path string) (interfaces.DB, error) {
	parts := strings.Split(path, "/")
	//subdir := strings.Join(parts[:len(parts)-1], "/")
	name := parts[len(parts)-1]
	if _, err := os.Stat(path); err == nil {
		if err := os.Chmod(path, 0777); err != nil {
			fmt.Printf("couldn't Chmod: %s", path)
			return nil, err
		}
	}

	opts := badger.DefaultOptions(path)
	db, err := badger.Open(opts)
	if err != nil {
		return nil, err
	}
	return newDB(db, path, name), nil
}

func newDB(db *badger.DB, dir string, dbName string) *BadgerDB {
	return &BadgerDB{
		DB:           db,
		Dir:          dir,
		dbName:       dbName,
		FreeListType: string(bolt.FreelistMapType), // dummy value
	}
}

type BadgerDB struct {
	DB           *badger.DB
	Dir          string
	dbName       string
	FreeListType string // no-opts
}

func (b *BadgerDB) RunGC(discardRatio float64) error {
	return b.DB.RunValueLogGC(discardRatio)
}

func (b *BadgerDB) Path() string {
	return b.Dir
}

func (b *BadgerDB) DBType() string {
	return "badger"
}

func (b *BadgerDB) GoString() string {
	return b.Dir + "/" + dbName
}

func (b *BadgerDB) String() string {
	return fmt.Sprintf("BadgerDB<%q>", b.Dir)
}

func (b *BadgerDB) Flatten() error {
	panic("not implemented for badger")
}

func (b *BadgerDB) Close() error {
	return b.DB.Close()
}

// Buckets no-opt
func (b *BadgerDB) Buckets() []string {
	return nil
}

// DeleteBucket no-opt
func (b *BadgerDB) DeleteBucket(name []byte) error {
	return nil
}

// HasBucket no-opt
func (b *BadgerDB) HasBucket(name string) bool {
	return false
}

// CreateBucket no-opt
func (b *BadgerDB) CreateBucket(name string) {
	return
}

func (b *BadgerDB) Begin(writable bool) (interfaces.Tx, error) {
	tx := b.DB.NewTransaction(writable)
	return &BadgerTx{tx: tx, writable: writable, db: b.DB, dir: b.Dir, dbName: b.dbName}, nil
}

func (b *BadgerDB) GetFromBucket(bucket string, key string) (val []byte) {
	tx := b.DB.NewTransaction(false)
	keyb := append([]byte(bucket), []byte(key)...)
	item, _ := tx.Get(keyb)
	v, _ := item.ValueCopy(nil)
	return v

}

func (b *BadgerDB) HashBuckets(ignores func(bucketName, keyName []byte) bool) (uint32, error) {
	panic("fix me")
}

func (b *BadgerDB) Size() (lsm int64) {
	l, d := b.DB.Size()
	return l + d
}

func (b *BadgerDB) Defrag(logger *zap.Logger, dbopts interface{}, defragLimit int) error {
	return nil
}

func (b *BadgerDB) Sync() error {
	return b.DB.Sync()
}

func (b *BadgerDB) Stats() interface{} {
	return nil
}

func (b *BadgerDB) Info() interface{} {
	return nil
}

func (b *BadgerDB) FreelistType() string {
	return b.FreeListType
}

func (b *BadgerDB) SetFreelistType(freeListType string) {
	b.FreeListType = freeListType
}

type BadgerTx struct {
	tx       *badger.Txn
	db       *badger.DB
	dir      string
	dbName   string
	writable bool
	size     int64
	mu       sync.Mutex
}

func (b *BadgerTx) DB() interfaces.DB {
	return newDB(b.db, b.dir, b.dbName)
}

func (b *BadgerTx) Size() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.size == 0 {
		return b.DB().Size()
	}
	return b.size
}

func (b *BadgerTx) Writable() bool {
	return b.writable
}

func (b *BadgerTx) Stats() interface{} {
	panic("implement me")
}

func resolveTableName(bucket string) string {
	tableName := bucket
	if tableName == "key" {
		tableName = "KVs"
	}
	return tableName
}

func (b *BadgerTx) Bucket(name []byte) interfaces.Bucket {
	tableName := resolveTableName(string(name))
	return &BadgerBucket{
		name:     tableName,
		db:       b.db,
		dbName:   b.dbName,
		TX:       b.tx,
		dir:      b.dir,
		writable: b.writable,
	}
}

func (b *BadgerTx) CreateBucket(name []byte) (interfaces.Bucket, error) {
	tableName := resolveTableName(string(name))
	return &BadgerBucket{
		name:     tableName,
		db:       b.db,
		dbName:   b.dbName,
		TX:       b.tx,
		dir:      b.dir,
		writable: b.writable,
	}, nil
}

func (b *BadgerTx) Observe(rebalanceHist, spillHist, writeHist prometheus.Histogram) {}

func (b *BadgerTx) DeleteBucket(name []byte) error {
	return nil
}

func (b *BadgerTx) ForEach(fn interface{}) error {
	panic("implement me")
}

func (b *BadgerTx) Commit() error {
	return b.tx.Commit()
}

func (b *BadgerTx) Rollback() error {
	b.tx.Discard()
	return nil
}

func (b *BadgerTx) Copy(w io.Writer) error {
	panic("fix me")
}

func (b *BadgerTx) WriteTo(w io.Writer) (n int64, err error) {
	panic("fix me")
}

func (b *BadgerTx) CopyDatabase(lg *zap.Logger, dst string) (err error) {
	panic("fix me")
}

type BadgerBucket struct {
	TX       *badger.Txn
	name     string
	dbName   string
	db       *badger.DB
	dir      string
	writable bool
}

func (b *BadgerBucket) Tx() interfaces.Tx {
	return &BadgerTx{
		tx:       b.TX,
		db:       b.db,
		dbName:   b.dbName,
		dir:      b.dir,
		writable: b.writable,
	}
}

func (b *BadgerBucket) Writable() bool {
	return b.writable
}

func (b *BadgerBucket) UnsafeRange(key, endKey []byte, limit int64) (keys [][]byte, vs [][]byte) {
	if limit <= 0 {
		limit = math.MaxInt64
	}
	bucketName := []byte(b.name)
	key = append(bucketName, key...)
	if len(endKey) != 0 {
		endKey = append(bucketName, endKey...)
	}

	var isMatch func(b []byte) bool
	if len(endKey) > 0 {
		isMatch = func(b []byte) bool { return bytes.Compare(b, endKey) < 0 }
	} else {
		isMatch = func(b []byte) bool { return bytes.Equal(b, key) }
		limit = 1
	}

	if len(endKey) == 0 {
		item, err := b.TX.Get(key)
		if err == badger.ErrKeyNotFound {
			return nil, nil
		}
		if err != nil {
			panic(err)
		}
		v, err := item.ValueCopy(nil)
		if err != nil {
			panic(err)
		}
		return [][]byte{item.KeyCopy(nil)}, [][]byte{v}
	}

	opt := badger.DefaultIteratorOptions
	if int(limit) > 0 && int(limit) < opt.PrefetchSize {
		opt.PrefetchSize = int(limit)
	}
	it := b.TX.NewIterator(opt)
	defer it.Close()
	for it.Seek(key); it.Valid(); it.Next() {
		if !isMatch(it.Item().Key()) {
			break
		}
		keys = append(keys, it.Item().KeyCopy(nil)[len(bucketName):])
		v, err := it.Item().ValueCopy(nil)
		if err != nil {
			panic(err)
		}
		vs = append(vs, v)

		if limit == int64(len(keys)) {
			break
		}
	}

	return keys, vs
}

func (b *BadgerBucket) SetFillPercent(fp float64) {
	return
}

func (b *BadgerBucket) Get(key []byte) []byte {
	key = append([]byte(b.name), key...)
	item, _ := b.TX.Get(key)
	v, _ := item.ValueCopy(nil)
	return v
}

func (b *BadgerBucket) Put(key []byte, value []byte) error {
	return b.TX.Set(append([]byte(b.name), key...), value)
}

func (b *BadgerBucket) Delete(key []byte) error {
	return b.TX.Delete(append([]byte(b.name), key...))
}

func (b *BadgerBucket) ForEach(fn func(k []byte, v []byte) error) error {
	it := b.TX.NewIterator(badger.DefaultIteratorOptions)
	defer it.Close()
	bucketName := []byte(b.name)

	for it.Seek(bucketName); it.ValidForPrefix(bucketName); it.Next() {
		item := it.Item()
		k := item.Key()
		v, err := item.ValueCopy(nil)
		if err != nil {
			panic(err)
		}
		fn(k[len(bucketName):], v)
	}
	return nil
}

func (b *BadgerBucket) Stats() interface{} {
	return nil
}
