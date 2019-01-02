package state

import (
	"fmt"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"
	"math/big"
)

// ----- Type
type accountProxiedBalance struct {
	ProxiedBalance        *big.Int
	DepositProxiedBalance *big.Int
}

func (a *accountProxiedBalance) String() (str string) {
	return fmt.Sprintf("pb: %v, dpb: %v", a.ProxiedBalance, a.DepositProxiedBalance)
}

func (a *accountProxiedBalance) Copy() *accountProxiedBalance {
	cpy := *a
	return &cpy
}

func (a *accountProxiedBalance) Equal(b *accountProxiedBalance) bool {
	if b == nil {
		return false
	}
	return a.ProxiedBalance.Cmp(b.ProxiedBalance) == 0 && a.DepositProxiedBalance.Cmp(b.DepositProxiedBalance) == 0
}

func NewAccountProxiedBalance() *accountProxiedBalance {
	return &accountProxiedBalance{
		ProxiedBalance:        big.NewInt(0),
		DepositProxiedBalance: big.NewInt(0),
	}
}

type Proxied map[common.Address]*accountProxiedBalance

func (p Proxied) String() (str string) {
	for key, value := range p {
		str += fmt.Sprintf("%X : %X\n", key, value)
	}
	return
}

func (p Proxied) Copy() Proxied {
	cpy := make(Proxied)
	for key, value := range p {
		cpy[key] = value.Copy()
	}
	return cpy
}

// ----- DelegateBalance

// AddDelegateBalance add amount to c's DelegateBalance.
func (c *stateObject) AddDelegateBalance(amount *big.Int) {
	// EIP158: We must check emptiness for the objects such that the account
	// clearing (0,0,0 objects) can take effect.
	if amount.Sign() == 0 {
		if c.empty() {
			c.touch()
		}
		return
	}
	c.SetDelegateBalance(new(big.Int).Add(c.DelegateBalance(), amount))
}

// SubDelegateBalance removes amount from c's DelegateBalance.
func (c *stateObject) SubDelegateBalance(amount *big.Int) {
	if amount.Sign() == 0 {
		return
	}
	c.SetDelegateBalance(new(big.Int).Sub(c.DelegateBalance(), amount))
}

func (self *stateObject) SetDelegateBalance(amount *big.Int) {
	self.db.journal = append(self.db.journal, delegateBalanceChange{
		account: &self.address,
		prev:    new(big.Int).Set(self.data.DelegateBalance),
	})
	self.setDelegateBalance(amount)
}

func (self *stateObject) setDelegateBalance(amount *big.Int) {
	self.data.DelegateBalance = amount
	if self.onDirty != nil {
		self.onDirty(self.Address())
		self.onDirty = nil
	}
}

func (self *stateObject) DelegateBalance() *big.Int {
	return self.data.DelegateBalance
}

// ----- ProxiedBalance

// AddProxiedBalance add amount to c's ProxiedBalance.
func (c *stateObject) AddProxiedBalance(amount *big.Int) {
	// EIP158: We must check emptiness for the objects such that the account
	// clearing (0,0,0 objects) can take effect.
	if amount.Sign() == 0 {
		if c.empty() {
			c.touch()
		}
		return
	}
	c.SetProxiedBalance(new(big.Int).Add(c.ProxiedBalance(), amount))
}

// SubProxiedBalance removes amount from c's ProxiedBalance.
func (c *stateObject) SubProxiedBalance(amount *big.Int) {
	if amount.Sign() == 0 {
		return
	}
	c.SetProxiedBalance(new(big.Int).Sub(c.ProxiedBalance(), amount))
}

func (self *stateObject) SetProxiedBalance(amount *big.Int) {
	self.db.journal = append(self.db.journal, proxiedBalanceChange{
		account: &self.address,
		prev:    new(big.Int).Set(self.data.ProxiedBalance),
	})
	self.setProxiedBalance(amount)
}

func (self *stateObject) setProxiedBalance(amount *big.Int) {
	self.data.ProxiedBalance = amount
	if self.onDirty != nil {
		self.onDirty(self.Address())
		self.onDirty = nil
	}
}

func (self *stateObject) ProxiedBalance() *big.Int {
	return self.data.ProxiedBalance
}

// ----- DepositProxiedBalance

// AddDepositProxiedBalance add amount to c's DepositProxiedBalance.
func (c *stateObject) AddDepositProxiedBalance(amount *big.Int) {
	// EIP158: We must check emptiness for the objects such that the account
	// clearing (0,0,0 objects) can take effect.
	if amount.Sign() == 0 {
		if c.empty() {
			c.touch()
		}
		return
	}
	c.SetDepositProxiedBalance(new(big.Int).Add(c.DepositProxiedBalance(), amount))
}

// SubDepositProxiedBalance removes amount from c's DepositProxiedBalance.
func (c *stateObject) SubDepositProxiedBalance(amount *big.Int) {
	if amount.Sign() == 0 {
		return
	}
	c.SetDepositProxiedBalance(new(big.Int).Sub(c.DepositProxiedBalance(), amount))
}

func (self *stateObject) SetDepositProxiedBalance(amount *big.Int) {
	self.db.journal = append(self.db.journal, depositProxiedBalanceChange{
		account: &self.address,
		prev:    new(big.Int).Set(self.data.DepositProxiedBalance),
	})
	self.setDepositProxiedBalance(amount)
}

func (self *stateObject) setDepositProxiedBalance(amount *big.Int) {
	self.data.DepositProxiedBalance = amount
	if self.onDirty != nil {
		self.onDirty(self.Address())
		self.onDirty = nil
	}
}

func (self *stateObject) DepositProxiedBalance() *big.Int {
	return self.data.DepositProxiedBalance
}

// ----- Delegate Trie

func (c *stateObject) getProxiedTrie(db Database) Trie {
	if c.proxiedTrie == nil {
		var err error
		c.proxiedTrie, err = db.OpenProxiedTrie(c.addrHash, c.data.ProxiedRoot)
		if err != nil {
			c.proxiedTrie, _ = db.OpenProxiedTrie(c.addrHash, common.Hash{})
			c.setError(fmt.Errorf("can't create proxied trie: %v", err))
		}
	}
	return c.proxiedTrie
}

// GetAccountProxiedBalance returns a value in proxied trie
func (self *stateObject) GetAccountProxiedBalance(db Database, key common.Address) *accountProxiedBalance {
	// If we have a dirty value for this state entry, return it
	value, dirty := self.dirtyProxied[key]
	if dirty {
		return value
	}
	// If we have the original value cached, return that
	value, cached := self.originProxied[key]
	if cached {
		return value
	}
	// Otherwise load the value from the database
	enc, err := self.getProxiedTrie(db).TryGet(key[:])
	if err != nil {
		self.setError(err)
		return nil
	}
	if len(enc) > 0 {
		value = new(accountProxiedBalance)
		err := rlp.DecodeBytes(enc, value)
		if err != nil {
			self.setError(err)
		}
	}
	self.originProxied[key] = value
	return value
}

// SetAccountProxiedBalance updates a value in account storage.
func (self *stateObject) SetAccountProxiedBalance(db Database, key common.Address, proxiedBalance *accountProxiedBalance) {
	self.db.journal = append(self.db.journal, accountProxiedBalanceChange{
		account:  &self.address,
		key:      key,
		prevalue: self.GetAccountProxiedBalance(db, key),
	})
	self.setAccountProxiedBalance(key, proxiedBalance)
}

func (self *stateObject) setAccountProxiedBalance(key common.Address, proxiedBalance *accountProxiedBalance) {
	self.dirtyProxied[key] = proxiedBalance

	if self.onDirty != nil {
		self.onDirty(self.Address())
		self.onDirty = nil
	}
}

// updateProxiedTrie writes cached proxied modifications into the object's proxied trie.
func (self *stateObject) updateProxiedTrie(db Database) Trie {
	tr := self.getProxiedTrie(db)
	for key, value := range self.dirtyProxied {
		delete(self.dirtyProxied, key)

		// Skip noop changes, persist actual changes
		if value.Equal(self.originProxied[key]) {
			continue
		}
		self.originProxied[key] = value

		if value == nil {
			self.setError(tr.TryDelete(key[:]))
			continue
		}
		// Encoding []byte cannot fail, ok to ignore the error.
		v, _ := rlp.EncodeToBytes(value)
		self.setError(tr.TryUpdate(key[:], v))
	}
	return tr
}

// updateProxiedRoot sets the proxiedTrie root to the current root hash of
func (self *stateObject) updateProxiedRoot(db Database) {
	self.updateProxiedTrie(db)
	self.data.ProxiedRoot = self.proxiedTrie.Hash()
}

// CommitProxiedTrie the proxied trie of the object to dwb.
// This updates the proxied trie root.
func (self *stateObject) CommitProxiedTrie(db Database) error {
	self.updateProxiedTrie(db)
	if self.dbErr != nil {
		return self.dbErr
	}
	root, err := self.proxiedTrie.Commit(nil)
	if err == nil {
		self.data.ProxiedRoot = root
	}
	return err
}
