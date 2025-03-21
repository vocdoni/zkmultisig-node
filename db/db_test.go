package db

import (
	"database/sql"
	"math/big"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/aragon/zkmultisig-node/test"
	"github.com/aragon/zkmultisig-node/types"
	qt "github.com/frankban/quicktest"
	"github.com/iden3/go-iden3-crypto/babyjub"
	_ "github.com/mattn/go-sqlite3"
	"github.com/vocdoni/arbo"
)

func TestStoreProcess(t *testing.T) {
	c := qt.New(t)

	db, err := sql.Open("sqlite3", filepath.Join(c.TempDir(), "testdb.sqlite3"))
	c.Assert(err, qt.IsNil)

	sqlite := NewSQLite(db)

	err = sqlite.Migrate()
	c.Assert(err, qt.IsNil)

	// prepare the votes
	processID := uint64(123)
	censusRoot := []byte("censusRoot")
	censusSize := uint64(100)
	ethBlockNum := uint64(10)
	resPubStartBlock := uint64(20)
	resPubWindow := uint64(20)
	minParticipation := uint8(20)
	minPositiveVotes := uint8(60)
	typ := uint8(1)

	err = sqlite.StoreProcess(processID, censusRoot, censusSize,
		ethBlockNum, resPubStartBlock, resPubWindow, minParticipation,
		minPositiveVotes, typ)
	c.Assert(err, qt.IsNil)

	// try to store the same processID, expecting error
	err = sqlite.StoreProcess(processID, censusRoot, censusSize,
		ethBlockNum, resPubStartBlock, resPubWindow, minParticipation,
		minPositiveVotes, typ)
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(err.Error(), qt.Equals, "UNIQUE constraint failed: processes.id")

	// try to store the a different processID, but the same censusRoot,
	// expecting no error
	err = sqlite.StoreProcess(processID+1, censusRoot, censusSize,
		ethBlockNum, resPubStartBlock, resPubWindow, minParticipation,
		minPositiveVotes, typ)
	c.Assert(err, qt.IsNil)

	process, err := sqlite.ReadProcessByID(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(process.ID, qt.Equals, processID)
	c.Assert(process.CensusRoot, qt.DeepEquals, censusRoot)
	c.Assert(process.EthBlockNum, qt.Equals, ethBlockNum)

	// read the stored votes
	processes, err := sqlite.ReadProcesses()
	c.Assert(err, qt.IsNil)
	c.Assert(len(processes), qt.Equals, 2)
	c.Assert(processes[0].ID, qt.Equals, processID)
	c.Assert(processes[0].CensusRoot, qt.DeepEquals, censusRoot)
	c.Assert(processes[0].EthBlockNum, qt.Equals, ethBlockNum)
}

func TestProcessStatus(t *testing.T) {
	c := qt.New(t)

	db, err := sql.Open("sqlite3", filepath.Join(c.TempDir(), "testdb.sqlite3"))
	c.Assert(err, qt.IsNil)

	sqlite := NewSQLite(db)

	err = sqlite.Migrate()
	c.Assert(err, qt.IsNil)

	// prepare the process
	processID := uint64(123)
	censusRoot := []byte("censusRoot")
	censusSize := uint64(100)
	ethBlockNum := uint64(10)
	resPubStartBlock := uint64(20)
	resPubWindow := uint64(20)
	minParticipation := uint8(60)
	minPositiveVotes := uint8(20)
	typ := uint8(1)

	err = sqlite.StoreProcess(processID, censusRoot, censusSize,
		ethBlockNum, resPubStartBlock, resPubWindow, minParticipation,
		minPositiveVotes, typ)
	c.Assert(err, qt.IsNil)

	status, err := sqlite.GetProcessStatus(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(status, qt.Equals, types.ProcessStatusOn)

	// update status to ProofGen
	err = sqlite.UpdateProcessStatus(processID, types.ProcessStatusProofGenerating)
	c.Assert(err, qt.IsNil)

	status, err = sqlite.GetProcessStatus(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(status, qt.Equals, types.ProcessStatusProofGenerating)

	// update status to Finished
	err = sqlite.UpdateProcessStatus(processID, types.ProcessStatusProofGenerated)
	c.Assert(err, qt.IsNil)

	status, err = sqlite.GetProcessStatus(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(status, qt.Equals, types.ProcessStatusProofGenerated)
}

func TestProcessesByStatus(t *testing.T) {
	c := qt.New(t)

	db, err := sql.Open("sqlite3", filepath.Join(c.TempDir(), "testdb.sqlite3"))
	c.Assert(err, qt.IsNil)

	sqlite := NewSQLite(db)

	err = sqlite.Migrate()
	c.Assert(err, qt.IsNil)

	censusRoot := []byte("censusRoot")
	censusSize := uint64(100)
	ethBlockNum := uint64(10)
	resPubStartBlock := uint64(20)
	resPubWindow := uint64(20)
	minParticipation := uint8(60)
	minPositiveVotes := uint8(20)
	typ := uint8(1)

	for i := 0; i < 10; i++ {
		err = sqlite.StoreProcess(uint64(i), censusRoot, censusSize,
			ethBlockNum, resPubStartBlock, resPubWindow, minParticipation,
			minPositiveVotes, typ)
		c.Assert(err, qt.IsNil)
	}

	processes, err := sqlite.ReadProcessesByStatus(types.ProcessStatusOn)
	c.Assert(err, qt.IsNil)
	c.Assert(len(processes), qt.Equals, 10)

	// update status to Closed for the first 4 processes
	for i := 0; i < 4; i++ {
		err = sqlite.UpdateProcessStatus(uint64(i), types.ProcessStatusFrozen)
		c.Assert(err, qt.IsNil)
	}

	processes, err = sqlite.ReadProcessesByStatus(types.ProcessStatusOn)
	c.Assert(err, qt.IsNil)
	c.Assert(len(processes), qt.Equals, 6)

	processes, err = sqlite.ReadProcessesByStatus(types.ProcessStatusFrozen)
	c.Assert(err, qt.IsNil)
	c.Assert(len(processes), qt.Equals, 4)
}

func TestProcessByResPubStartBlock(t *testing.T) {
	c := qt.New(t)

	db, err := sql.Open("sqlite3", filepath.Join(c.TempDir(), "testdb.sqlite3"))
	c.Assert(err, qt.IsNil)

	sqlite := NewSQLite(db)

	err = sqlite.Migrate()
	c.Assert(err, qt.IsNil)

	processID := uint64(123)
	censusRoot := []byte("censusRoot")
	censusSize := uint64(100)
	ethBlockNum := uint64(10)
	resPubStartBlock := uint64(20)
	resPubWindow := uint64(20)
	minParticipation := uint8(60)
	minPositiveVotes := uint8(20)
	typ := uint8(1)

	err = sqlite.StoreProcess(processID, censusRoot, censusSize,
		ethBlockNum, resPubStartBlock, resPubWindow, minParticipation,
		minPositiveVotes, typ)
	c.Assert(err, qt.IsNil)
	err = sqlite.StoreProcess(processID+1, censusRoot, censusSize,
		ethBlockNum, resPubStartBlock, resPubWindow, minParticipation,
		minPositiveVotes, typ)
	c.Assert(err, qt.IsNil)
	err = sqlite.StoreProcess(processID+2, censusRoot, censusSize,
		ethBlockNum, resPubStartBlock, resPubWindow, minParticipation,
		minPositiveVotes, typ)
	c.Assert(err, qt.IsNil)
	err = sqlite.StoreProcess(processID+3, censusRoot, censusSize,
		ethBlockNum, resPubStartBlock, resPubWindow, minParticipation,
		minPositiveVotes, typ)
	c.Assert(err, qt.IsNil)
	err = sqlite.StoreProcess(processID+4, censusRoot, censusSize,
		ethBlockNum, resPubStartBlock+1, resPubWindow, minParticipation,
		minPositiveVotes, typ)
	c.Assert(err, qt.IsNil)
	err = sqlite.StoreProcess(processID+5, censusRoot, censusSize,
		ethBlockNum, resPubStartBlock+1, resPubWindow, minParticipation,
		minPositiveVotes, typ)
	c.Assert(err, qt.IsNil)

	processes, err := sqlite.ReadProcesses()
	c.Assert(err, qt.IsNil)
	c.Assert(len(processes), qt.Equals, 6)

	processes, err = sqlite.ReadProcessesByResPubStartBlock(resPubStartBlock)
	c.Assert(err, qt.IsNil)
	c.Assert(len(processes), qt.Equals, 4)

	processes, err = sqlite.ReadProcessesByResPubStartBlock(resPubStartBlock + 1)
	c.Assert(err, qt.IsNil)
	c.Assert(len(processes), qt.Equals, 2)
}

func TestStoreAndReadVotes(t *testing.T) {
	c := qt.New(t)

	db, err := sql.Open("sqlite3", filepath.Join(c.TempDir(), "testdb.sqlite3"))
	c.Assert(err, qt.IsNil)

	sqlite := NewSQLite(db)

	err = sqlite.Migrate()
	c.Assert(err, qt.IsNil)

	// try to store a vote for a processID that does not exist yet
	vote := []byte("test")
	voteBI := arbo.BytesToBigInt(vote)
	keys := test.GenUserKeys(1)
	sig := keys.PrivateKeys[0].SignPoseidon(voteBI)
	votePackage := types.VotePackage{
		Signature: sig.Compress(),
		CensusProof: types.CensusProof{
			Index:       1,
			PublicKey:   &keys.PublicKeys[0],
			Weight:      big.NewInt(1),
			MerkleProof: []byte("test"),
		},
		Vote: []byte("test"),
	}
	// expect error when storing the vote, as processID does not exist yet
	err = sqlite.StoreVotePackage(uint64(123), votePackage)
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(err.Error(), qt.Equals, "Can not store VotePackage, ProcessID=123 does not exist")

	// store a processID in which the votes will be related
	processID := uint64(123)
	censusRoot := []byte("censusRoot")
	censusSize := uint64(100)
	ethBlockNum := uint64(10)
	resPubStartBlock := uint64(20)
	resPubWindow := uint64(20)
	minParticipation := uint8(60)
	minPositiveVotes := uint8(20)
	typ := uint8(1)

	err = sqlite.StoreProcess(processID, censusRoot, censusSize,
		ethBlockNum, resPubStartBlock, resPubWindow, minParticipation,
		minPositiveVotes, typ)
	c.Assert(err, qt.IsNil)

	// prepare the votes
	nVotes := 10

	var votesAdded []types.VotePackage
	for i := 0; i < nVotes; i++ {
		voteBytes := []byte("test")
		voteBI := arbo.BytesToBigInt(voteBytes)
		sk := babyjub.NewRandPrivKey()
		pubK := sk.Public()
		sigUncomp := sk.SignPoseidon(voteBI)
		sig := sigUncomp.Compress()
		vote := types.VotePackage{
			Signature: sig,
			CensusProof: types.CensusProof{
				Index:       uint64(i),
				PublicKey:   pubK,
				Weight:      big.NewInt(1),
				MerkleProof: []byte("test" + strconv.Itoa(i)),
			},
			Vote: voteBytes,
		}
		votesAdded = append(votesAdded, vote)

		err = sqlite.StoreVotePackage(processID, vote)
		c.Assert(err, qt.IsNil)
	}

	// try to store a vote with already stored index
	err = sqlite.StoreVotePackage(processID, votesAdded[0])
	c.Assert(err, qt.Not(qt.IsNil))
	c.Assert(err.Error(), qt.Equals, "UNIQUE constraint failed: votepackages.indx")

	// read the stored votes
	votes, err := sqlite.ReadVotePackagesByProcessID(processID)
	c.Assert(err, qt.IsNil)
	c.Assert(len(votes), qt.Equals, nVotes)
}

func TestFrozeProcessesByCurrentBlockNum(t *testing.T) {
	c := qt.New(t)

	db, err := sql.Open("sqlite3", filepath.Join(c.TempDir(), "testdb.sqlite3"))
	c.Assert(err, qt.IsNil)

	sqlite := NewSQLite(db)

	err = sqlite.Migrate()
	c.Assert(err, qt.IsNil)

	processID := uint64(123)
	censusRoot := []byte("censusRoot")
	censusSize := uint64(100)
	ethBlockNum := uint64(10)
	resPubStartBlock := uint64(20)
	resPubWindow := uint64(20)
	minParticipation := uint8(60)
	minPositiveVotes := uint8(20)
	typ := uint8(1)

	// first, store few processes
	for i := 0; i < 10; i++ {
		err = sqlite.StoreProcess(processID+uint64(i), censusRoot,
			censusSize, ethBlockNum, resPubStartBlock+uint64(i), resPubWindow,
			minParticipation, minPositiveVotes, typ)
		c.Assert(err, qt.IsNil)
	}

	// expect 10 total processes
	processes, err := sqlite.ReadProcesses()
	c.Assert(err, qt.IsNil)
	c.Assert(len(processes), qt.Equals, 10)

	// expect 10 processes with status = types.ProcessStatusOn
	processes, err = sqlite.ReadProcessesByStatus(types.ProcessStatusOn)
	c.Assert(err, qt.IsNil)
	c.Assert(len(processes), qt.Equals, 10)

	// simulate that the blockchain advances up to block i0, i1, ..., i5 (6 in total)
	err = sqlite.FrozeProcessesByCurrentBlockNum(resPubStartBlock + 5)
	c.Assert(err, qt.IsNil)

	// expect 4 processes with status = types.ProcessStatusOn
	processes, err = sqlite.ReadProcessesByStatus(types.ProcessStatusOn)
	c.Assert(err, qt.IsNil)
	c.Assert(len(processes), qt.Equals, 4)

	// expect 6 processes with status = types.ProcessStatusFrozen
	processes, err = sqlite.ReadProcessesByStatus(types.ProcessStatusFrozen)
	c.Assert(err, qt.IsNil)
	c.Assert(len(processes), qt.Equals, 6)
}

func TestMetaTable(t *testing.T) {
	c := qt.New(t)

	db, err := sql.Open("sqlite3", filepath.Join(c.TempDir(), "testdb.sqlite3"))
	c.Assert(err, qt.IsNil)

	sqlite := NewSQLite(db)

	err = sqlite.Migrate()
	c.Assert(err, qt.IsNil)

	err = sqlite.InitMeta(42, 0)
	c.Assert(err, qt.IsNil)

	b, err := sqlite.GetLastSyncBlockNum()
	c.Assert(err, qt.IsNil)
	c.Assert(b, qt.Equals, uint64(0))

	err = sqlite.UpdateLastSyncBlockNum(1234)
	c.Assert(err, qt.IsNil)

	b, err = sqlite.GetLastSyncBlockNum()
	c.Assert(err, qt.IsNil)
	c.Assert(b, qt.Equals, uint64(1234))
}
