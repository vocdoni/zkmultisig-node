package eth

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math/big"

	"github.com/aragon/zkmultisig-node/db"
	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/vocdoni/arbo"
	"go.vocdoni.io/dvote/log"
)

const (
	// eventNewProcessLen defines the length of an event log of newProcess
	eventNewProcessLen = 320 // = 32*10
	// eventResultPublishedLen defines the length of an event log of
	// resultPublished
	eventResultPublishedLen = 160 // = 32*5
	// eventProcessClosedLen defines the length of an event log of
	// processClosed
	eventProcessClosedLen = 96 // = 32*3
)

// ClientInterf defines the interface that synchronizes with the Ethereum
// blockchain to obtain the processes data
type ClientInterf interface {
	// Sync scans the contract activity since the given fromBlock until the
	// current block, storing in the database all the updates on the
	// Processess
	Start(fromBlock uint64) error
}

// Client implements the ClientInterf that reads data from the Ethereum
// blockchain
type Client struct {
	client       *ethclient.Client
	db           *db.SQLite
	contractAddr common.Address
	ChainID      uint64
}

// Options is used to pass the parameters to load a new Client
type Options struct {
	EthURL       string
	SQLite       *db.SQLite
	ContractAddr common.Address
}

// New loads a new Client
func New(opts Options) (*Client, error) {
	client, err := ethclient.Dial(opts.EthURL)
	if err != nil {
		log.Error(err)
		return nil, err
	}

	// get network ChainID
	chainID, err := client.ChainID(context.Background())
	if err != nil {
		return nil, err
	}

	return &Client{
		client:       client,
		db:           opts.SQLite,
		contractAddr: opts.ContractAddr,
		ChainID:      chainID.Uint64(),
	}, nil
}

// Sync synchronizes the blocknums and events since the last synced block to
// the current one, and then live syncs the new ones
func (c *Client) Sync() error {
	// TODO WARNING:
	// Probably the logic will need to be changed to support reorgs of
	// chain. Maybe wait to sync blocks until some new blocks after the
	// block have been created.

	// get lastSyncBlockNum from db
	lastSyncBlockNum, err := c.db.GetLastSyncBlockNum()
	if err != nil {
		return err
	}

	// start live sync events (before synchronizing the history)
	go c.syncEventsLive() // nolint:errcheck

	// sync from lastSyncBlockNum until the current blocknum
	err = c.syncHistory(lastSyncBlockNum)
	if err != nil {
		return err
	}

	// live sync blocks
	err = c.syncBlocksLive()
	if err != nil {
		return err
	}
	return nil
}

// syncBlocksLive synchronizes live the ethereum blocks
func (c *Client) syncBlocksLive() error {
	// sync to new blocks
	headers := make(chan *types.Header)
	sub, err := c.client.SubscribeNewHead(context.Background(), headers)
	if err != nil {
		log.Error(err)
		return err
	}

	for {
		select {
		case err := <-sub.Err():
			log.Error(err)
		case header := <-headers:
			log.Debugf("new eth block received: %d", header.Number.Uint64())
			// store in db lastSyncBlockNum
			err = c.db.UpdateLastSyncBlockNum(header.Number.Uint64())
			if err != nil {
				log.Error(err)
			}
		}
	}
}

// syncEventsLive synchronizes live from the zkmultisig contract events
func (c *Client) syncEventsLive() error {
	query := ethereum.FilterQuery{
		Addresses: []common.Address{c.contractAddr},
	}

	logs := make(chan types.Log)
	sub, err := c.client.SubscribeFilterLogs(context.Background(), query, logs)
	if err != nil {
		log.Error(err)
		return err
	}

	for {
		select {
		case err := <-sub.Err():
			log.Error(err)
		case vLog := <-logs:
			err = c.processEventLog(vLog)
			if err != nil {
				log.Error(err)
			}
		}
	}
}

// syncHistory synchronizes from the zkmultisig contract the events & blockNums
// from the given block to the current block height.
func (c *Client) syncHistory(startBlock uint64) error {
	header, err := c.client.HeaderByNumber(context.Background(), nil)
	if err != nil {
		log.Error(err)
		return err
	}
	currBlockNum := header.Number
	log.Debugf("[SyncHistory] blocks from: %d, to: %d", startBlock, currBlockNum)
	err = c.syncEventsHistory(big.NewInt(int64(startBlock)), currBlockNum)
	if err != nil {
		log.Error(err)
		return err
	}

	// update the processes which their ResPubStartBlock has been reached
	// (and that they were still in status ProcessStatusOn
	err = c.db.FrozeProcessesByCurrentBlockNum(currBlockNum.Uint64())
	if err != nil {
		log.Error(err)
		return err
	}
	// TODO take into account chain reorgs: for currBlockNum, set to
	// ProcessStatusOn the processes with resPubStartBlock>currBlockNum
	return nil
}

// syncEventsHistory synchronizes from the zkmultisig contract log events
// between the given startBlock and endBlock
func (c *Client) syncEventsHistory(startBlock, endBlock *big.Int) error {
	query := ethereum.FilterQuery{
		FromBlock: startBlock,
		ToBlock:   endBlock,
		Addresses: []common.Address{
			c.contractAddr,
		},
	}
	logs, err := c.client.FilterLogs(context.Background(), query)
	if err != nil {
		log.Error(err)
		return err
	}
	for i := 0; i < len(logs); i++ {
		err = c.processEventLog(logs[i])
		if err != nil {
			log.Error(err)
		}
	}

	return nil
}

func (c *Client) processEventLog(eventLog types.Log) error {
	// depending on eventLog.Data length, parse the different types of
	// event logs
	switch l := len(eventLog.Data); l {
	case eventNewProcessLen:
		e, err := parseEventNewProcess(eventLog.Data)
		if err != nil {
			return fmt.Errorf("blocknum: %d, error parsing event log"+
				" (newProcess): %x, err: %s",
				eventLog.BlockNumber, eventLog.Data, err)
		}
		log.Debugf("Event: (blocknum: %d) %s",
			eventLog.BlockNumber, e)
		// store the process in the db
		err = c.db.StoreProcess(e.ProcessID, e.CensusRoot[:], e.CensusSize,
			eventLog.BlockNumber, e.ResPubStartBlock, e.ResPubWindow,
			e.MinParticipation, e.MinPositiveVotes, e.Type)
		if err != nil {
			return fmt.Errorf("error storing new process: %x, err: %s",
				eventLog.Data, err)
		}
	case eventResultPublishedLen:
		e, err := parseEventResultPublished(eventLog.Data)
		if err != nil {
			return fmt.Errorf("blocknum: %d, error parsing event log"+
				" (resultPublished): %x, err: %s",
				eventLog.BlockNumber, eventLog.Data, err)
		}
		log.Debugf("Event: (blocknum: %d) %s",
			eventLog.BlockNumber, e)
	case eventProcessClosedLen:
		e, err := parseEventProcessClosed(eventLog.Data)
		if err != nil {
			return fmt.Errorf("blocknum: %d, error parsing event log"+
				" (processClosed): %x, err: %s",
				eventLog.BlockNumber, eventLog.Data, err)
		}
		log.Debugf("Event: (blocknum: %d) %s",
			eventLog.BlockNumber, e)
	default:
		fmt.Printf("LOG in block %d:\n %x \n", eventLog.BlockNumber, eventLog.Data)
		return fmt.Errorf("unrecognized event log with length %d", l)
	}

	return nil
}

// eventNewProcess contains the data received from an event log of newProcess
type eventNewProcess struct {
	Creator          common.Address
	ProcessID        uint64
	TxHash           [32]byte
	CensusRoot       [32]byte
	CensusSize       uint64
	ResPubStartBlock uint64
	ResPubWindow     uint64
	MinParticipation uint8
	MinPositiveVotes uint8
	Type             uint8
}

// String implements the String interface for eventNewProcess
func (e *eventNewProcess) String() string {
	return fmt.Sprintf("[eventNewProcess]: Creator %s, ProcessID: %d, TxHash: %s,"+
		" CensusRoot: %s, Type: %d, CensusSize: %d, ResPubStartBlock: %d,"+
		" ResPubWindow: %d, MinParticipation: %d, MinPositiveVotes: %d",
		e.Creator, e.ProcessID, hex.EncodeToString(e.TxHash[:]),
		arbo.BytesToBigInt(e.CensusRoot[:]), e.Type, e.CensusSize,
		e.ResPubStartBlock, e.ResPubWindow, e.MinParticipation,
		e.MinPositiveVotes)
}

func parseEventNewProcess(d []byte) (*eventNewProcess, error) {
	if len(d) != eventNewProcessLen {
		return nil, fmt.Errorf("newProcess event log should be of length %d, current: %d",
			eventNewProcessLen, len(d))
	}
	var e eventNewProcess

	// contract event:
	// event EventProcessCreated(address creator, uint256 id,uint256
	// transactionHash,  uint256 censusRoot, uint8 typ, uint64 censusSize,
	// uint64 resPubStartBlock, uint64 resPubWindow, uint8
	// minParticipation, uint8 minPositiveVotes);

	creatorBytes := d[:32]
	e.Creator = common.BytesToAddress(creatorBytes[12:32])

	// WARNING for the moment is uint256 but probably will change to uint64
	// idBytes := new(big.Int).SetBytes(idBytes)
	idBytes := d[64-8 : 64] // uint64
	e.ProcessID = binary.BigEndian.Uint64(idBytes)

	copy(e.TxHash[:], d[64:96])

	// note that here Ethereum returns the CensusRoot in big endian
	copy(e.CensusRoot[:], arbo.SwapEndianness(d[96:128]))

	censusSizeBytes := d[160-8 : 160] // uint64
	e.CensusSize = binary.BigEndian.Uint64(censusSizeBytes)

	resPubStartBlockBytes := d[192-8 : 192] // uint64
	e.ResPubStartBlock = binary.BigEndian.Uint64(resPubStartBlockBytes)

	resPubWindowBytes := d[224-8 : 224] // uint64
	e.ResPubWindow = binary.BigEndian.Uint64(resPubWindowBytes)

	e.MinParticipation = uint8(d[255])
	e.MinPositiveVotes = uint8(d[287])
	e.Type = uint8(d[319])

	return &e, nil
}

type eventResultPublished struct {
	Publisher    common.Address
	ProcessID    uint64
	ReceiptsRoot [32]byte
	Result       uint64
	NVotes       uint64
}

// String implements the String interface for eventResultPublished
func (e *eventResultPublished) String() string {
	return fmt.Sprintf("[eventResultPublished]: Publisher %s, ProcessID: %d,"+
		" ReceiptsRoot: %s, Result: %d, NVotes: %d",
		e.Publisher, e.ProcessID,
		arbo.BytesToBigInt(e.ReceiptsRoot[:]), e.Result, e.NVotes)
}

func parseEventResultPublished(d []byte) (*eventResultPublished, error) {
	if len(d) != eventResultPublishedLen {
		return nil, fmt.Errorf("resultPublished event log should be of"+
			" length %d, current: %d", eventResultPublishedLen, len(d))
	}

	// event EventResultPublished(address publisher, uint256 id, uint256
	// receiptsRoot, uint64 result, uint64 nVotes);

	var e eventResultPublished

	publisherBytes := d[:32]
	e.Publisher = common.BytesToAddress(publisherBytes[12:32])

	idBytes := d[64-8 : 64] // uint64
	e.ProcessID = binary.BigEndian.Uint64(idBytes)

	// note that here Ethereum returns the CensusRoot in big endian
	copy(e.ReceiptsRoot[:], arbo.SwapEndianness(d[64:96]))

	result := d[128-8 : 128] // uint64
	e.Result = binary.BigEndian.Uint64(result)

	nVotes := d[160-8 : 160] // uint64
	e.NVotes = binary.BigEndian.Uint64(nVotes)

	return &e, nil
}

type eventProcessClosed struct {
	Caller    common.Address
	ProcessID uint64
	Success   bool
}

// String implements the String interface for eventProcessClosed
func (e *eventProcessClosed) String() string {
	return fmt.Sprintf("[eventProcessClosed]: Caller %s, ProcessID: %d, Success: %t",
		e.Caller, e.ProcessID, e.Success)
}

// event EventProcessClosed(address caller, uint256 id, bool success);
func parseEventProcessClosed(d []byte) (*eventProcessClosed, error) {
	if len(d) != eventProcessClosedLen {
		return nil, fmt.Errorf("processClosed event log should be of length %d, current: %d",
			eventProcessClosedLen, len(d))
	}

	var e eventProcessClosed

	creatorBytes := d[:32]
	e.Caller = common.BytesToAddress(creatorBytes[12:32])

	idBytes := d[64-8 : 64] // uint64
	e.ProcessID = binary.BigEndian.Uint64(idBytes)

	success := d[96-1 : 96] // uint64
	if success[0] == byte(1) {
		e.Success = true
	}

	return &e, nil
}
