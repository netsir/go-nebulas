// Copyright (C) 2017 go-nebulas authors
//
// This file is part of the go-nebulas library.
//
// the go-nebulas library is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// the go-nebulas library is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with the go-nebulas library.  If not, see <http://www.gnu.org/licenses/>.
//

package state

import (
	"errors"
	"fmt"

	"github.com/gogo/protobuf/proto"
	"github.com/nebulasio/go-nebulas/common/trie"
	"github.com/nebulasio/go-nebulas/core/pb"
	"github.com/nebulasio/go-nebulas/storage"
	"github.com/nebulasio/go-nebulas/util"
	"github.com/nebulasio/go-nebulas/util/byteutils"
	log "github.com/sirupsen/logrus"
)

// Errors
var (
	ErrBalanceInsufficient = errors.New("cannot subtract a value which is bigger than current balance")
	ErrAccountNotFound     = errors.New("cannot found account in storage")
)

// account info in state Trie
type account struct {
	balance *util.Uint128
	nonce   uint64
	// UserType: Global Storage
	// ContractType: Local Storage
	variables *trie.BatchTrie
	// ContractType: Transaction Hash
	birthPlace byteutils.Hash
}

// ToBytes converts domain Account to bytes
func (acc *account) ToBytes() ([]byte, error) {
	value, err := acc.balance.ToFixedSizeByteSlice()
	if err != nil {
		return nil, err
	}
	pbAcc := &corepb.Account{
		Balance:    value,
		Nonce:      acc.nonce,
		VarsHash:   acc.variables.RootHash(),
		BirthPlace: acc.birthPlace,
	}
	bytes, err := proto.Marshal(pbAcc)
	if err != nil {
		return nil, err
	}
	return bytes, nil
}

// FromBytes converts bytes to Account
func (acc *account) FromBytes(bytes []byte, storage storage.Storage) error {
	pbAcc := &corepb.Account{}
	if err := proto.Unmarshal(bytes, pbAcc); err != nil {
		return err
	}
	value, err := util.NewUint128FromFixedSizeByteSlice(pbAcc.Balance)
	if err != nil {
		return err
	}
	acc.balance = value
	acc.nonce = pbAcc.Nonce
	acc.birthPlace = pbAcc.BirthPlace
	acc.variables, err = trie.NewBatchTrie(pbAcc.VarsHash, storage)
	if err != nil {
		return err
	}
	return nil
}

// Balance return account's balance
func (acc *account) Balance() *util.Uint128 {
	return acc.balance
}

// Nonce return account's nonce
func (acc *account) Nonce() uint64 {
	return acc.nonce
}

// VarsHash return account's variables hash
func (acc *account) VarsHash() byteutils.Hash {
	return acc.variables.RootHash()
}

// BirthPlace return account's birth place
func (acc *account) BirthPlace() byteutils.Hash {
	return acc.birthPlace
}

// BeginBatch begins a batch task
func (acc *account) BeginBatch() {
	log.Info("Account Begin.")
	acc.variables.BeginBatch()
}

// Commit a batch task
func (acc *account) Commit() {
	acc.variables.Commit()
	log.WithFields(log.Fields{
		"acc": acc,
	}).Info("Account Commit.")
}

// RollBack a batch task
func (acc *account) RollBack() {
	acc.variables.RollBack()
	log.WithFields(log.Fields{
		"acc": acc,
	}).Info("Account RollBack.")
}

// IncreNonce by 1
func (acc *account) IncreNonce() {
	acc.nonce++
}

// AddBalance to an account
func (acc *account) AddBalance(value *util.Uint128) {
	acc.balance.Add(acc.balance.Int, value.Int)
}

// SubBalance to an account
func (acc *account) SubBalance(value *util.Uint128) error {
	if acc.balance.Cmp(value.Int) < 0 {
		return ErrBalanceInsufficient
	}
	acc.balance.Sub(acc.balance.Int, value.Int)
	return nil
}

// Put into account's storage
func (acc *account) Put(key []byte, value []byte) error {
	_, err := acc.variables.Put(key, value)
	return err
}

// Get from account's storage
func (acc *account) Get(key []byte) ([]byte, error) {
	return acc.variables.Get(key)
}

// Del from account's storage
func (acc *account) Del(key []byte) error {
	if _, err := acc.variables.Del(key); err != nil {
		return err
	}
	return nil
}

// Iterator map var from account's storage
func (acc *account) Iterator(prefix []byte) (Iterator, error) {
	return acc.variables.Iterator(prefix)
}

func (acc *account) String() string {
	return fmt.Sprintf("Account %p {Balance:%v; Nonce:%v; VarsHash:%v; BirthPlace:%v}",
		acc,
		acc.balance.Int,
		acc.nonce,
		byteutils.Hex(acc.variables.RootHash()),
		acc.birthPlace.Hex(),
	)
}

// AccountState manage account state in Block
type accountState struct {
	stateTrie    *trie.BatchTrie
	dirtyAccount map[byteutils.HexHash]Account
	batching     bool
	storage      storage.Storage
}

// NewAccountState create a new account state
func NewAccountState(root byteutils.Hash, storage storage.Storage) (AccountState, error) {
	stateTrie, err := trie.NewBatchTrie(root, storage)
	if err != nil {
		return nil, err
	}
	return &accountState{
		stateTrie:    stateTrie,
		dirtyAccount: make(map[byteutils.HexHash]Account),
		batching:     false,
		storage:      storage,
	}, nil
}

func (as *accountState) recordDirtyAccount(addr byteutils.Hash, acc Account) {
	if as.batching {
		acc.BeginBatch()
		as.dirtyAccount[addr.Hex()] = acc
	}
}

func (as *accountState) newAccount(addr byteutils.Hash, birthPlace byteutils.Hash) Account {
	varTrie, _ := trie.NewBatchTrie(nil, as.storage)
	acc := &account{
		balance:    util.NewUint128(),
		nonce:      0,
		variables:  varTrie,
		birthPlace: birthPlace,
	}
	as.recordDirtyAccount(addr, acc)
	return acc
}

func (as *accountState) getAccount(addr byteutils.Hash) (Account, error) {
	// search in dirty account
	if acc, ok := as.dirtyAccount[addr.Hex()]; ok {
		return acc, nil
	}
	// search in storage
	bytes, err := as.stateTrie.Get(addr)
	if err == nil {
		acc := new(account)
		err = acc.FromBytes(bytes, as.storage)
		if err != nil {
			return nil, err
		}
		as.recordDirtyAccount(addr, acc)
		return acc, nil
	}
	return nil, ErrAccountNotFound
}

// RootHash return root hash of account state
func (as *accountState) RootHash() byteutils.Hash {
	for addr, acc := range as.dirtyAccount {
		bytes, _ := acc.ToBytes()
		key, _ := addr.Hash()
		as.stateTrie.Put(key, bytes)
	}
	return as.stateTrie.RootHash()
}

// GetOrCreateUserAccount according to the addr
func (as *accountState) GetOrCreateUserAccount(addr []byte) Account {
	acc, err := as.getAccount(addr)
	if err != nil {
		acc := as.newAccount(addr, nil)
		return acc
	}
	return acc
}

// GetContractAccount from current AccountState
func (as *accountState) GetContractAccount(addr []byte) (Account, error) {
	acc, err := as.getAccount(addr)
	if err != nil {
		return nil, err
	}
	return acc, nil
}

// CreateContractAccount according to the addr, and set birthPlace as creation tx hash
func (as *accountState) CreateContractAccount(addr []byte, birthPlace []byte) (Account, error) {
	acc := as.newAccount(addr, birthPlace)
	return acc, nil
}

// BeginBatch begin a batch task
func (as *accountState) BeginBatch() {
	log.Info("AccountState Begin.")
	as.batching = true
	err := as.stateTrie.BeginBatch()
	if err != nil {
		log.Error(err)
	}
}

// Commit a batch task
func (as *accountState) Commit() {
	for addr, acc := range as.dirtyAccount {
		acc.Commit()
		delete(as.dirtyAccount, addr)
		bytes, _ := acc.ToBytes()
		key, _ := addr.Hash()
		as.stateTrie.Put(key, bytes)
	}
	as.stateTrie.Commit()
	as.batching = false
	log.WithFields(log.Fields{
		"AccountState": as,
	}).Info("AccountState Commit.")
}

// RollBack a batch task
func (as *accountState) RollBack() {
	as.stateTrie.RollBack()
	for addr, acc := range as.dirtyAccount {
		acc.RollBack()
		delete(as.dirtyAccount, addr)
	}
	as.batching = false
	log.WithFields(log.Fields{
		"AccountState": as,
	}).Info("AccountState RollBack.")
}

// Clone an accountState
func (as *accountState) Clone() (AccountState, error) {
	stateTrie, err := as.stateTrie.Clone()
	if err != nil {
		return nil, err
	}
	return &accountState{
		stateTrie:    stateTrie,
		dirtyAccount: as.dirtyAccount,
		batching:     as.batching,
		storage:      as.storage,
	}, nil
}

func (as *accountState) String() string {
	return fmt.Sprintf("AccountState %p {RootHash:%s; dirtyAccount:%v; Batching:%v; Storage:%p}",
		as,
		byteutils.Hex(as.stateTrie.RootHash()),
		as.dirtyAccount,
		as.batching,
		as.storage,
	)
}
