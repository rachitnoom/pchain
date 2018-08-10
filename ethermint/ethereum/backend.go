package ethereum

import (
	"bytes"
	"fmt"
	"math/big"
	"os"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core"
	"github.com/ethereum/go-ethereum/core/state"
	ethTypes "github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/eth"
	"github.com/ethereum/go-ethereum/node"
	"github.com/ethereum/go-ethereum/p2p"
	"github.com/ethereum/go-ethereum/rpc"

	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/vm"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/pchain/common/plogger"
	emtTypes "github.com/pchain/ethermint/types"
	"github.com/syndtr/goleveldb/leveldb/errors"
	abciTypes "github.com/tendermint/abci/types"
	"github.com/tendermint/tendermint/rpc/core/types"
)

var logger = plogger.GetLogger("ethermint-ethereum")

const TRANSACTION_NUM_LIMIT = 200000

// used by Backend to call tendermint rpc endpoints
// TODO: replace with HttpClient https://github.com/tendermint/go-rpc/issues/8
type Client interface {
	// see tendermint/go-rpc/client/http_client.go:115 func (c *ClientURI) Call(...)
	Call(method string, params map[string]interface{}, result interface{}) (interface{}, error)
}

// Intermediate state of a block, updated with each DeliverTx and reset on Commit
type work struct {
	header  *ethTypes.Header
	parent  *ethTypes.Block
	state   *state.StateDB
	config  *params.ChainConfig
	chainDb ethdb.Database

	txIndex      int
	transactions []*ethTypes.Transaction
	receipts     ethTypes.Receipts
	allLogs      []*ethTypes.Log

	totalUsedGas   *big.Int
	totalUsedMoney *big.Int
	rewardPerBlock *big.Int
	gp             *core.GasPool

	//emmark for pre-check
	pcGp      *core.GasPool
	pcBalance map[vm.Account]*big.Int
	txCount   *big.Int
}

type pending struct {
	commitMutex *sync.Mutex
	work        *work
}

// Backend handles the chain database and VM
type Backend struct {
	ethereum *eth.Ethereum
	pending  *pending
	client   Client
	config   *eth.Config
}

const (
	maxWaitForServerRetries = 10
)

// New creates a new Backend
func NewBackend(ctx *node.ServiceContext, config *eth.Config, client Client, cch core.CrossChainHelper) (*Backend, error) {
	p := &pending{commitMutex: &sync.Mutex{}}

	ethereum, err := eth.New(ctx, config, p, client, cch)
	if err != nil {
		return nil, err
	}
	ethereum.BlockChain().SetValidator(NullBlockProcessor{})
	ethBackend := &Backend{
		ethereum: ethereum,
		pending:  p,
		client:   client,
		config:   config,
	}

	return ethBackend, nil
}

func waitForServer(s *Backend) error {
	// wait for Tendermint to open the socket and run http endpoint
	var result core_types.TMResult
	retriesCount := 0
	for result == nil {
		_, err := s.client.Call("status", map[string]interface{}{}, &result)
		if err != nil {
			logger.Infof("Waiting for tendermint endpoint to start: %s", err)
		}
		if retriesCount += 1; retriesCount >= maxWaitForServerRetries {
			return abciTypes.ErrInternalError
		}
		time.Sleep(time.Second)
	}
	return nil
}

//----------------------------------------------------------------------

// we must implement our own net service since we don't have access to `internal/ethapi`
type NetRPCService struct {
	networkVersion int
}

func (n *NetRPCService) Version() string {
	return fmt.Sprintf("%d", n.networkVersion)
}

// Listening returns an indication if the node is listening for network connections.
func (s *NetRPCService) Listening() bool {
	return true // always listening
}

// PeerCount returns the number of connected peers
func (s *NetRPCService) PeerCount() hexutil.Uint {
	return 0
}

type MinerRPCService struct {
	networkVersion int
}

// APIs returns the collection of RPC services the ethereum package offers.
func (s *Backend) APIs() []rpc.API {
	apis := s.Ethereum().APIs()
	retApis := []rpc.API{}
	for _, v := range apis {
		//emmark

		if v.Namespace == "net" {
			networkVersion := 1
			v.Service = &NetRPCService{networkVersion}
		}
		/*
			if v.Namespace == "miner" {
				continue
			}
			if _, ok := v.Service.(*eth.PublicMinerAPI); ok {
				continue
			}
		*/
		retApis = append(retApis, v)
	}

	go s.txBroadcastLoop()

	/*
		//add by author@liaoyd
		go s.validatorTransLoop()
	*/
	apis = retApis

	return retApis
}

// Start implements node.Service, starting all internal goroutines needed by the
// Ethereum protocol implementation.
func (s *Backend) Start(srvr *p2p.Server) error {
	return nil
}

// Stop implements node.Service, terminating all internal goroutines used by the
// Ethereum protocol.
func (s *Backend) Stop() error {
	s.ethereum.Stop()
	return nil
}

// Protocols implements node.Service, returning all the currently configured
// network protocols to start.
func (s *Backend) Protocols() []p2p.Protocol {
	return nil
}

// Ethereum returns the underlying the ethereum object
func (s *Backend) Ethereum() *eth.Ethereum {
	return s.ethereum
}

// Config returns the eth.Config
func (s *Backend) Config() *eth.Config {
	return s.config
}

//----------------------------------------------------------------------
// Transactions sent via the go-ethereum rpc need to be routed to tendermint

// listen for txs and forward to tendermint
// TODO: some way to exit this (it runs in a go-routine)
func (s *Backend) txBroadcastLoop() {
	txSub := s.ethereum.EventMux().Subscribe(core.TxPreEvent{})

	if err := waitForServer(s); err != nil {
		// timeouted when waiting for tendermint communication failed
		logger.Errorf("Failed to run tendermint HTTP endpoint, err=%s", err)
		os.Exit(1)
	}

	for obj := range txSub.Chan() {
		event := obj.Data.(core.TxPreEvent)
		if err := s.BroadcastTx(event.Tx); err != nil {
			logger.Errorf("Broadcast, err=%s", err)
		}
	}
}

// BroadcastTx broadcasts a transaction to tendermint core
func (s *Backend) BroadcastTx(tx *ethTypes.Transaction) error {
	var result core_types.TMResult
	buf := new(bytes.Buffer)
	if err := tx.EncodeRLP(buf); err != nil {
		return err
	}
	params := map[string]interface{}{
		"tx": buf.Bytes(),
	}
	_, err := s.client.Call("broadcast_tx_sync", params, &result)
	return err
}

//----------------------------------------------------------------------

func (s *pending) Pending() (*ethTypes.Block, *state.StateDB) {
	s.commitMutex.Lock()
	defer s.commitMutex.Unlock()

	return ethTypes.NewBlock(
		s.work.header,
		s.work.transactions,
		nil,
		s.work.receipts,
	), s.work.state.Copy()
}

func (s *pending) PendingBlock() *ethTypes.Block {
	s.commitMutex.Lock()
	defer s.commitMutex.Unlock()

	return ethTypes.NewBlock(
		s.work.header,
		s.work.transactions,
		nil,
		s.work.receipts,
	)
}

//emmark----------------------------------------------------------------
func (b *Backend) SetPreCheckInt(pcInt eth.PreCheckInt) {
	b.ethereum.SetPreCheckInt(pcInt)
}

func (b *Backend) PreCheck(tx *ethTypes.Transaction) error {
	return b.pending.preCheck(b.ethereum.BlockChain(), b.config, tx)
}

func (p *pending) preCheck(blockchain *core.BlockChain, config *eth.Config, tx *ethTypes.Transaction) error {
	p.commitMutex.Lock()
	defer p.commitMutex.Unlock()

	blockHash := common.Hash{}
	return p.work.preCheck(blockchain, config, blockHash, tx)
}

func (w *work) preCheck(blockchain *core.BlockChain, config *eth.Config, blockHash common.Hash, tx *ethTypes.Transaction) error {

	/*
		if(w.txCount.Cmp(big.NewInt(TRANSACTION_NUM_LIMIT)) > 0) {
			return fmt.Errorf("transactions are too much for one block round, reached 1000 tx")
		}
	*/
	w.txCount.Add(w.txCount, big.NewInt(1))

	fmt.Printf("(w *work) preCheck(), checked %v transaction in one block\n", w.txCount)

	msg, err := tx.AsMessage(ethTypes.MakeSigner(config.ChainConfig, w.header.Number))
	if err != nil {
		return err
	}

	fmt.Printf("(w *work) preCheck(); w.header is %s\n", w.header.String())
	//fake related w.header params
	if w.header.Difficulty == nil {
		w.header.Difficulty = new(big.Int).SetInt64(184108136445)
	}
	if w.header.Time == nil {
		w.header.Time = new(big.Int).SetInt64(time.Now().Unix())
	}
	//fmt.Printf("(w *work) preCheck(); w.header is %s\n", w.header.String())
	senderAddress := msg.From()
	if !w.state.Exist(senderAddress) {
		err = fmt.Errorf("(w *work) preCheck(); sender does not exist")
		return err
	}
	/*
		senderAccount := w.state.GetAccount(senderAddress)

		// Pre-pay gas

		mgas := msg.Gas()
		mgval := new(big.Int).Mul(mgas, msg.GasPrice())

		if _, exist := w.pcBalance[senderAccount]; !exist {
			balance := senderAccount.Balance()
			fmt.Printf("(w *work) preCheck(); balance is %v\n", balance)
			w.pcBalance[senderAccount] = balance
			fmt.Printf("(w *work) preCheck(); w.pcBalance[senderAccount] is %v\n", w.pcBalance[senderAccount])
		}

		fmt.Printf("(w *work) preCheck(); before pre-sub, senderAccount %s has balance %v, gaslimit is now %v\n"+
			"gas is %v, spending is %v\n",
			senderAddress, w.pcBalance[senderAccount], w.pcGp, mgas, mgval)

		if senderAccount.Balance().Cmp(mgval) < 0 {
			err = fmt.Errorf("insufficient ETH for gas (%x). Req %v, has %v", senderAddress.Bytes()[:4], mgval, senderAccount.Balance())
		}
		w.pcBalance[senderAccount].Sub(w.pcBalance[senderAccount], mgval)

		if err := w.pcGp.SubGas(mgas); err != nil {
			if core.IsGasLimitErr(err) {
				return err
			}
			return core.InvalidTxError(err)
		}
		fmt.Printf("(w *work) preCheck(); after sub, senderAddress %s has balance %v, gaslimit is now %v\n",
			senderAddress, w.pcBalance[senderAccount], w.pcGp, mgas, mgval)
	*/
	return nil
}

//----------------------------------------------------------------------

func (b *Backend) DeliverTx(tx *ethTypes.Transaction) error {
	return b.pending.deliverTx(b.ethereum.BlockChain(), b.config,
		tx, b.Ethereum().ApiBackend.GetCrossChainHelper())
}

func (p *pending) deliverTx(blockchain *core.BlockChain, config *eth.Config,
	tx *ethTypes.Transaction, cch core.CrossChainHelper) error {
	p.commitMutex.Lock()
	defer p.commitMutex.Unlock()

	blockHash := common.Hash{}
	return p.work.deliverTx(blockchain, config, blockHash, tx, cch)
}

func (w *work) deliverTx(blockchain *core.BlockChain, config *eth.Config, blockHash common.Hash,
	tx *ethTypes.Transaction, cch core.CrossChainHelper) error {
	w.state.StartRecord(tx.Hash(), blockHash, w.txIndex)
	fmt.Printf("(w *work) deliverTx(); before apply transaction, w.gp is %v\n", w.gp)
	receipt, _, err := core.ApplyTransactionEx(
		config.ChainConfig,
		blockchain,
		w.gp,
		w.state,
		w.header,
		tx,
		w.totalUsedGas,
		w.totalUsedMoney,
		vm.Config{EnablePreimageRecording: config.EnablePreimageRecording},
		cch,
	)
	if err != nil {
		return err
		logger.Debugf("DeliverTx error: %v", err)
		return abciTypes.ErrInternalError
	}
	fmt.Printf("(w *work) deliverTx(); after apply transaction, w.gp is %v\n", w.gp)
	logs := w.state.GetLogs(tx.Hash())

	w.txIndex += 1

	w.transactions = append(w.transactions, tx)
	w.receipts = append(w.receipts, receipt)
	w.allLogs = append(w.allLogs, logs...)

	return err
}

//----------------------------------------------------------------------
// Check if any Child Chain in DB match the launch criteria
func (b *Backend) CheckAndProcessChildChain(height uint64) {
	// Child Chain should not check this
	if b.config.ChainConfig.PChainId == "pchain" {
		b.pending.checkAndProcessChildChain(height, b.ethereum.ApiBackend.GetCrossChainHelper())
	}
}

func (p *pending) checkAndProcessChildChain(height uint64, cch core.CrossChainHelper) {
	p.commitMutex.Lock()
	defer p.commitMutex.Unlock()

	p.work.checkAndProcessChildChain(height, cch)
}

func (w *work) checkAndProcessChildChain(height uint64, cch core.CrossChainHelper) {
	cch.ReadyForLaunchChildChain(height, w.state)
}

//----------------------------------------------------------------------
// Refund the Validator Locked Balance
func (b *Backend) RefundValidatorLockedBalance(refund []*abciTypes.RefundValidatorAmount) {
	b.pending.refundValidatorLockedBalance(refund)
}

func (p *pending) refundValidatorLockedBalance(refund []*abciTypes.RefundValidatorAmount) {
	p.commitMutex.Lock()
	defer p.commitMutex.Unlock()

	p.work.refundValidatorLockedBalance(refund)
}

func (w *work) refundValidatorLockedBalance(refund []*abciTypes.RefundValidatorAmount) {
	for _, r := range refund {
		w.state.SubLockedBalance(r.Address, r.Amount)
		w.state.AddBalance(r.Address, r.Amount)
	}
}

//----------------------------------------------------------------------

func (b *Backend) AccumulateRewards(strategy emtTypes.Strategy, rewardPerBlock *big.Int) {
	b.pending.accumulateRewards(strategy, rewardPerBlock)
}

func (p *pending) accumulateRewards(strategy emtTypes.Strategy, rewardPerBlock *big.Int) {
	p.commitMutex.Lock()
	defer p.commitMutex.Unlock()
	// set the epoch reward per block
	p.work.rewardPerBlock = rewardPerBlock
	p.work.accumulateRewards(strategy)
}

func (w *work) accumulateRewards(strategy emtTypes.Strategy) {
	if !(w.totalUsedMoney.Sign() == 0 && w.rewardPerBlock.Sign() == 0) {
		strategy.AccumulateRewards(w.state, w.header, []*ethTypes.Header{}, w.totalUsedMoney, w.rewardPerBlock)
	}

	w.header.GasUsed = w.totalUsedGas
}

//----------------------------------------------------------------------

func (b *Backend) Commit(receiver common.Address) (common.Hash, error) {
	return b.pending.commit(b.ethereum.BlockChain(), b.ethereum.ChainDb(), receiver)
}

func (p *pending) commit(blockchain *core.BlockChain, chainDb ethdb.Database, receiver common.Address) (common.Hash, error) {
	p.commitMutex.Lock()
	defer p.commitMutex.Unlock()

	blockHash, err := p.work.commit(blockchain)
	if err != nil {
		return common.Hash{}, err
	}

	work, err := p.resetWork(blockchain, chainDb, receiver)
	if err != nil {
		return common.Hash{}, err
	}

	p.work = work
	return blockHash, err
}

func (w *work) commit(blockchain *core.BlockChain) (common.Hash, error) {
	// commit ethereum state and update the header
	/*
		hashArray, err := w.state.Commit(false) // XXX: ugh hardforks
		if err != nil {
			return common.Hash{}, err
		}
		w.header.Root = hashArray

		// tag logs with state root
		// NOTE: BlockHash ?
		for _, log := range w.allLogs {
			log.BlockHash = hashArray
		}

		// save the block to disk
		glog.V(logger.Debug).Infof("Committing block with state hash %X and root hash %X", hashArray, blockHash)
		_, err = blockchain.InsertChain([]*ethTypes.Block{block})
		if err != nil {
			glog.V(logger.Debug).Infof("Error inserting ethereum block in chain: %v", err)
			return common.Hash{}, err
		}
	*/

	// create block object and compute final commit hash (hash of the ethereum block)
	hashArray, err := w.state.Commit(false)

	w.header.Root = hashArray
	if err != nil {
		return common.Hash{}, err
	}

	block := ethTypes.NewBlock(w.header, w.transactions, nil, w.receipts)
	blockHash := block.Hash()

	fmt.Printf("(w *work) commit(), commit %v transactions in one block\n", len(w.transactions))

	parent := blockchain.GetBlock(block.ParentHash(), block.NumberU64()-1)
	if parent == nil {
		logger.Errorln("Invalid block found during mining")
		return common.Hash{}, errors.New("Invalid block found during mining")
	}

	auxValidator := blockchain.AuxValidator()
	if err := core.ValidateHeader(w.config, auxValidator, block.Header(), parent.Header(), true, false); err != nil && err != core.BlockFutureErr {
		logger.Errorln("Invalid header on mined block:", err)
		return common.Hash{}, err
	}

	stat, err := blockchain.WriteBlock(block)
	if err != nil {
		logger.Errorln("error writing block to chain", err)
		return common.Hash{}, err
	}

	// update block hash since it is now available and not when the receipt/log of individual transactions were created
	for _, r := range w.receipts {
		for _, l := range r.Logs {
			l.BlockHash = block.Hash()
		}
	}

	for _, log := range w.state.Logs() {
		log.BlockHash = block.Hash()
	}

	// check if canon block and write transactions
	if stat == core.CanonStatTy {
		//fmt.Printf("(w *work) commit() stat == core.CanonStatTy\n")
		// This puts transactions in a extra db for rpc
		core.WriteTransactions(w.chainDb, block)
		// store the receipts
		core.WriteReceipts(w.chainDb, w.receipts)
		// Write map map bloom filters
		core.WriteMipmapBloom(w.chainDb, block.NumberU64(), w.receipts)
		// implicit by posting ChainHeadEvent
		//mustCommitNewWork = false
	}

	// broadcast before waiting for validation
	go func(block *ethTypes.Block, logs []*ethTypes.Log, receipts []*ethTypes.Receipt) {
		if err := core.WriteBlockReceipts(w.chainDb, block.Hash(), block.NumberU64(), receipts); err != nil {
			logger.Warnln("error writing block receipts:", err)
		}
	}(block, w.state.Logs(), w.receipts)

	return blockHash, err
}

//----------------------------------------------------------------------

func (b *Backend) ResetWork(receiver common.Address) error {
	work, err := b.pending.resetWork(b.ethereum.BlockChain(), b.ethereum.ChainDb(), receiver)
	b.pending.work = work
	return err
}

func (p *pending) resetWork(blockchain *core.BlockChain, chainDb ethdb.Database, receiver common.Address) (*work, error) {
	state, err := blockchain.State()
	if err != nil {
		return nil, err
	}

	currentBlock := blockchain.CurrentBlock()
	ethHeader := newBlockHeader(receiver, currentBlock)

	return &work{
		header:         ethHeader,
		parent:         currentBlock,
		state:          state,
		config:         blockchain.Config(),
		chainDb:        chainDb,
		txIndex:        0,
		totalUsedGas:   big.NewInt(0),
		totalUsedMoney: big.NewInt(0),
		gp:             new(core.GasPool).AddGas(ethHeader.GasLimit),
		pcGp:           new(core.GasPool).AddGas(ethHeader.GasLimit),
		pcBalance:      make(map[vm.Account]*big.Int),
		txCount:        big.NewInt(0),
	}, nil
}

//----------------------------------------------------------------------

func (b *Backend) UpdateHeaderWithTimeInfo(tmHeader *abciTypes.Header) {
	b.pending.updateHeaderWithTimeInfo(b.Config().ChainConfig, tmHeader.Time)
}

func (p *pending) updateHeaderWithTimeInfo(config *params.ChainConfig, parentTime uint64) {
	p.commitMutex.Lock()
	defer p.commitMutex.Unlock()

	p.work.updateHeaderWithTimeInfo(config, parentTime)
}

func (w *work) updateHeaderWithTimeInfo(config *params.ChainConfig, parentTime uint64) {
	lastBlock := w.parent
	w.header.Time = new(big.Int).SetUint64(parentTime)
	w.header.Difficulty = core.CalcDifficulty(config, parentTime,
		lastBlock.Time().Uint64(), lastBlock.Number(), lastBlock.Difficulty())
}

//----------------------------------------------------------------------

func newBlockHeader(receiver common.Address, prevBlock *ethTypes.Block) *ethTypes.Header {
	return &ethTypes.Header{
		Number:     prevBlock.Number().Add(prevBlock.Number(), big.NewInt(1)),
		ParentHash: prevBlock.Hash(),
		GasLimit:   core.CalcGasLimit(prevBlock),
		Coinbase:   receiver,
	}
}

/*
//----------------------
//author@liaoyd
func (s *Backend) validatorTransLoop() {
	fmt.Println("func (s *Backend) validatorTransLoop()")
	exSub := s.ethereum.EventMux().Subscribe(core.ValidatorEvent{})

	if err := waitForServer(s); err != nil {
		// timeouted when waiting for tendermint communication failed
		glog.V(logger.Error).Infof("Failed to run tendermint HTTP endpoint, err=%s", err)
		os.Exit(1)
	}

	var result core_types.TMResult
	for obj := range exSub.Chan() {
		event := obj.Data.(core.ValidatorEvent)
		fmt.Println("event in extransloop!!!", event)
		if event.Flag == "VALIDATORS" {
			s.client.Call("validators", map[string]interface{}{}, &result)
			continue
		}
		params := map[string]interface{}{
			"epoch":  event.Epoch,
			"key":    event.Key,
			"power":  event.Power,
			"flag":   event.Flag,
		}
		_, err := s.client.Call("validator_opera", params, &result)
		if err != nil {
			fmt.Println(err)
		}
	}
}
*/
