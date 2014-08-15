// Copyright (c) 2013, Suryandaru Triandana <syndtr@gmail.com>
// All rights reserved.
//
// Use of this source code is governed by a BSD-style license that can be
// found in the LICENSE file.

package leveldb

import (
	"sync/atomic"

	"github.com/syndtr/goleveldb/leveldb/journal"
	"github.com/syndtr/goleveldb/leveldb/memdb"
	"github.com/syndtr/goleveldb/leveldb/util"
)

type memDB struct {
	pool *util.Pool
	db   *memdb.DB
	ref  int32
}

func (m *memDB) incref() {
	atomic.AddInt32(&m.ref, 1)
}

func (m *memDB) decref() {
	if ref := atomic.AddInt32(&m.ref, -1); ref == 0 {
		m.pool.Put(m)
	} else if ref < 0 {
		panic("negative memdb ref")
	}
}

// Get latest sequence number.
func (db *DB) getSeq() uint64 {
	return atomic.LoadUint64(&db.seq)
}

// Atomically adds delta to seq.
func (db *DB) addSeq(delta uint64) {
	atomic.AddUint64(&db.seq, delta)
}

// Create new memdb and froze the old one; need external synchronization.
// newMem only called synchronously by the writer.
func (db *DB) newMem(n int) (mem *memDB, err error) {
	num := db.s.allocFileNum()
	file := db.s.getJournalFile(num)
	w, err := file.Create()
	if err != nil {
		db.s.reuseFileNum(num)
		return
	}

	db.memMu.Lock()
	defer db.memMu.Unlock()

	if db.frozenMem != nil {
		panic("still has frozen mem")
	}

	if db.journal == nil {
		db.journal = journal.NewWriter(w)
	} else {
		db.journal.Reset(w)
		db.journalWriter.Close()
		db.frozenJournalFile = db.journalFile
	}
	db.journalWriter = w
	db.journalFile = file
	db.frozenMem = db.mem
	mem, ok := db.memPool.Get().(*memDB)
	if ok && mem.db.Capacity() >= n {
		mem.db.Reset()
		mem.incref()
	} else {
		mem = &memDB{
			pool: db.memPool,
			db:   memdb.New(db.s.icmp, maxInt(db.s.o.GetWriteBuffer(), n)),
			ref:  1,
		}
	}
	mem.incref()
	db.mem = mem
	// The seq only incremented by the writer. And whoever called newMem
	// should hold write lock, so no need additional synchronization here.
	db.frozenSeq = db.seq
	return
}

// Get all memdbs.
func (db *DB) getMems() (e, f *memDB) {
	db.memMu.RLock()
	defer db.memMu.RUnlock()
	if db.mem == nil {
		panic("nil effective mem")
	}
	db.mem.incref()
	if db.frozenMem != nil {
		db.frozenMem.incref()
	}
	return db.mem, db.frozenMem
}

// Get frozen memdb.
func (db *DB) getEffectiveMem() *memDB {
	db.memMu.RLock()
	defer db.memMu.RUnlock()
	if db.mem == nil {
		panic("nil effective mem")
	}
	db.mem.incref()
	return db.mem
}

// Check whether we has frozen memdb.
func (db *DB) hasFrozenMem() bool {
	db.memMu.RLock()
	defer db.memMu.RUnlock()
	return db.frozenMem != nil
}

// Get frozen memdb.
func (db *DB) getFrozenMem() *memDB {
	db.memMu.RLock()
	defer db.memMu.RUnlock()
	if db.frozenMem != nil {
		db.frozenMem.incref()
	}
	return db.frozenMem
}

// Drop frozen memdb; assume that frozen memdb isn't nil.
func (db *DB) dropFrozenMem() {
	db.memMu.Lock()
	if err := db.frozenJournalFile.Remove(); err != nil {
		db.logf("journal@remove removing @%d %q", db.frozenJournalFile.Num(), err)
	} else {
		db.logf("journal@remove removed @%d", db.frozenJournalFile.Num())
	}
	db.frozenJournalFile = nil
	db.frozenMem.decref()
	db.frozenMem = nil
	db.memMu.Unlock()
}

// Set closed flag; return true if not already closed.
func (db *DB) setClosed() bool {
	return atomic.CompareAndSwapUint32(&db.closed, 0, 1)
}

// Check whether DB was closed.
func (db *DB) isClosed() bool {
	return atomic.LoadUint32(&db.closed) != 0
}

// Check read ok status.
func (db *DB) ok() error {
	if db.isClosed() {
		return ErrClosed
	}
	return nil
}
