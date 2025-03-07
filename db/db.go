package db

import (
	"database/sql"
	"errors"
	"fmt"
	"math/big"

	"github.com/aragon/zkmultisig-node/types"
)

var (
	// ErrMetaNotInDB is used to indicate when metadata (which includes
	// lastSyncBlockNum) is not stored in the db
	ErrMetaNotInDB = fmt.Errorf("Meta does not exist in the db")
)

// SQLite represents the SQLite database
type SQLite struct {
	db *sql.DB
}

// NewSQLite returns a new *SQLite database
func NewSQLite(db *sql.DB) *SQLite {
	return &SQLite{
		db: db,
	}
}

// Migrate creates the tables needed for the database
func (r *SQLite) Migrate() error {
	query := `
	PRAGMA foreign_keys = ON;
	`
	_, err := r.db.Exec(query)
	if err != nil {
		return err
	}

	query = `
	CREATE TABLE IF NOT EXISTS processes(
		id INTEGER NOT NULL PRIMARY KEY UNIQUE,
		status INTEGER NOT NULL,
		censusRoot BLOB NOT NULL,
		censusSize INTEGER NOT NULL,
		ethBlockNum INTEGER NOT NULL,
		resPubStartBlock INTEGER NOT NULL,
		resPubWindow INTEGER NOT NULL,
		minParticipation INTEGER NOT NULL,
		minPositiveVotes INTEGER NOT NULL,
		type INTEGER NOT NULL,
		insertedDatetime DATETIME
	);
	`
	_, err = r.db.Exec(query)
	if err != nil {
		return err
	}

	query = `
	CREATE TABLE IF NOT EXISTS votepackages(
		indx INTEGER NOT NULL PRIMARY KEY UNIQUE,
		publicKey BLOB NOT NULL UNIQUE,
		weight BLOB NOT NULL,
		merkleproof BLOB NOT NULL UNIQUE,
		signature BLOB NOT NULL,
		vote BLOB NOT NULL,
		insertedDatetime DATETIME,
		processID INTEGER NOT NULL,
		FOREIGN KEY(processID) REFERENCES processes(id)
	);
	`
	_, err = r.db.Exec(query)
	if err != nil {
		return err
	}

	query = `
	CREATE TABLE IF NOT EXISTS meta(
		id INTEGER NOT NULL PRIMARY KEY AUTOINCREMENT,
		chainID INTEGER NOT NULL,
		lastSyncBlockNum INTEGER NOT NULL,
		lastUpdate DATETIME
	);
	`
	_, err = r.db.Exec(query)
	if err != nil {
		return err
	}

	return nil
}

// StoreProcess stores a new process with the given id, censusRoot and
// ethBlockNum. When a new process is stored, it's assumed that it comes from
// the SmartContract, and its status is set to types.ProcessStatusOn
func (r *SQLite) StoreProcess(id uint64, censusRoot []byte, censusSize,
	ethBlockNum, resPubStartBlock, resPubWindow uint64, minParticipation,
	minPositiveVotes, typ uint8) error {
	sqlQuery := `
	INSERT INTO processes(
		id,
		status,
		censusRoot,
		censusSize,
		ethBlockNum,
		resPubStartBlock,
		resPubWindow,
		minParticipation,
		minPositiveVotes,
		type,
		insertedDatetime
	) values(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	`

	stmt, err := r.db.Prepare(sqlQuery)
	if err != nil {
		return err
	}
	defer stmt.Close() //nolint:errcheck

	_, err = stmt.Exec(id, types.ProcessStatusOn, censusRoot, censusSize,
		ethBlockNum, resPubStartBlock, resPubWindow, minParticipation,
		minPositiveVotes, typ)
	if err != nil {
		return err
	}
	return nil
}

// UpdateProcessStatus sets the given types.ProcessStatus for the given id.
// This method should only be called when updating from SmartContracts.
func (r *SQLite) UpdateProcessStatus(id uint64, status types.ProcessStatus) error {
	sqlQuery := `
	UPDATE processes SET status=? WHERE id=?
	`

	stmt, err := r.db.Prepare(sqlQuery)
	if err != nil {
		return err
	}
	defer stmt.Close() //nolint:errcheck

	_, err = stmt.Exec(int(status), id)
	if err != nil {
		return err
	}
	return nil
}

// GetProcessStatus returns the stored types.ProcessStatus for the given id
func (r *SQLite) GetProcessStatus(id uint64) (types.ProcessStatus, error) {
	row := r.db.QueryRow("SELECT status FROM processes WHERE id = ?", id)

	var status int
	err := row.Scan(&status)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("Process ID:%d, does not exist in the db", id)
		}
		return 0, err
	}
	return types.ProcessStatus(status), nil
}

// ReadProcessByID reads the types.Process by the given id
func (r *SQLite) ReadProcessByID(id uint64) (*types.Process, error) {
	row := r.db.QueryRow("SELECT * FROM processes WHERE id = ?", id)

	var process types.Process
	err := row.Scan(&process.ID, &process.Status, &process.CensusRoot,
		&process.CensusSize, &process.EthBlockNum, &process.ResPubStartBlock,
		&process.ResPubWindow, &process.MinParticipation,
		&process.MinPositiveVotes, &process.Type, &process.InsertedDatetime)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("Process ID:%d, does not exist in the db", id)
		}
		return nil, err
	}
	return &process, nil
}

// ReadProcesses reads all the stored types.Process
func (r *SQLite) ReadProcesses() ([]types.Process, error) {
	sqlQuery := `
	SELECT * FROM processes	ORDER BY datetime(insertedDatetime) DESC
	`
	// TODO maybe, in all affected methods, order by EthBlockNum (creation)
	// instead of insertedDatetime.

	rows, err := r.db.Query(sqlQuery)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var processes []types.Process
	for rows.Next() {
		process := types.Process{}
		err = rows.Scan(&process.ID, &process.Status,
			&process.CensusRoot, &process.CensusSize, &process.EthBlockNum,
			&process.ResPubStartBlock, &process.ResPubWindow,
			&process.MinParticipation, &process.MinPositiveVotes,
			&process.Type, &process.InsertedDatetime)
		if err != nil {
			return nil, err
		}
		processes = append(processes, process)
	}
	return processes, nil
}

// FrozeProcessesByCurrentBlockNum sets the process status to
// ProcessStatusFrozen for all the processes that: have their
// status==ProcessStatusOn and that their ResPubStartBlock <= currentBlockNum.
// This method is intended to be used by the eth.Client when synchronizing
// processes to the last block number.
func (r *SQLite) FrozeProcessesByCurrentBlockNum(currBlockNum uint64) error {
	sqlQuery := `
	UPDATE processes
	SET status = ?
	WHERE (resPubStartBlock <= ? AND status = ?)
	`

	stmt, err := r.db.Prepare(sqlQuery)
	if err != nil {
		return err
	}
	defer stmt.Close() //nolint:errcheck

	_, err = stmt.Exec(types.ProcessStatusFrozen,
		int(currBlockNum), types.ProcessStatusOn)
	if err != nil {
		return err
	}
	return nil
}

// ReadProcessesByResPubStartBlock reads all the stored processes which contain
// the given ResPubStartBlock
func (r *SQLite) ReadProcessesByResPubStartBlock(resPubStartBlock uint64) (
	[]types.Process, error) {
	sqlQuery := `
	SELECT * FROM processes WHERE resPubStartBlock = ?
	ORDER BY datetime(resPubStartBlock) DESC
	`

	rows, err := r.db.Query(sqlQuery, resPubStartBlock)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var processes []types.Process
	for rows.Next() {
		process := types.Process{}
		err = rows.Scan(&process.ID, &process.Status,
			&process.CensusRoot, &process.CensusSize, &process.EthBlockNum,
			&process.ResPubStartBlock, &process.ResPubWindow,
			&process.MinParticipation, &process.MinPositiveVotes,
			&process.Type, &process.InsertedDatetime)
		if err != nil {
			return nil, err
		}
		processes = append(processes, process)
	}
	return processes, nil
}

// ReadProcessesByStatus reads all the stored processes which have the given
// status
func (r *SQLite) ReadProcessesByStatus(status types.ProcessStatus) ([]types.Process, error) {
	sqlQuery := `
	SELECT * FROM processes WHERE status = ?
	ORDER BY datetime(insertedDatetime) DESC
	`

	rows, err := r.db.Query(sqlQuery, status)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var processes []types.Process
	for rows.Next() {
		process := types.Process{}
		err = rows.Scan(&process.ID, &process.Status,
			&process.CensusRoot, &process.CensusSize, &process.EthBlockNum,
			&process.ResPubStartBlock, &process.ResPubWindow,
			&process.MinParticipation, &process.MinPositiveVotes,
			&process.Type, &process.InsertedDatetime)
		if err != nil {
			return nil, err
		}
		processes = append(processes, process)
	}
	return processes, nil
}

// StoreVotePackage stores the given types.VotePackage for the given CensusRoot
func (r *SQLite) StoreVotePackage(processID uint64, vote types.VotePackage) error {
	// TODO check that processID exists
	sqlQuery := `
	INSERT INTO votepackages(
		indx,
		publicKey,
		weight,
		merkleproof,
		signature,
		vote,
		insertedDatetime,
		processID
	) values(?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?)
	`

	stmt, err := r.db.Prepare(sqlQuery)
	if err != nil {
		return err
	}
	defer stmt.Close() //nolint:errcheck

	if vote.CensusProof.Weight == nil {
		// no weight defined, use 0
		vote.CensusProof.Weight = big.NewInt(0)
	}

	_, err = stmt.Exec(vote.CensusProof.Index, vote.CensusProof.PublicKey,
		vote.CensusProof.Weight.Bytes(), vote.CensusProof.MerkleProof,
		vote.Signature[:], vote.Vote, processID)
	if err != nil {
		if err.Error() == "FOREIGN KEY constraint failed" {
			return fmt.Errorf("Can not store VotePackage, ProcessID=%d does not exist", processID)
		}
		return err
	}
	return nil
}

// ReadVotePackagesByProcessID reads all the stored types.VotePackage for the
// given ProcessID. VotePackages returned are sorted by index parameter, from
// smaller to bigger.
func (r *SQLite) ReadVotePackagesByProcessID(processID uint64) ([]types.VotePackage, error) {
	// TODO add pagination
	sqlQuery := `
	SELECT signature, indx, publicKey, weight, merkleproof, vote FROM votepackages
	WHERE processID = ?
	ORDER BY indx ASC
	`

	rows, err := r.db.Query(sqlQuery, processID)
	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var votes []types.VotePackage
	var weightBytes []byte
	for rows.Next() {
		vote := types.VotePackage{}
		var sigBytes []byte
		err = rows.Scan(&sigBytes, &vote.CensusProof.Index,
			&vote.CensusProof.PublicKey, &weightBytes,
			&vote.CensusProof.MerkleProof, &vote.Vote)
		if err != nil {
			return nil, err
		}
		vote.CensusProof.Weight = new(big.Int).SetBytes(weightBytes)
		copy(vote.Signature[:], sigBytes)
		votes = append(votes, vote)
	}
	return votes, nil
}

// InitMeta initializes the meta table with the given chainID
func (r *SQLite) InitMeta(chainID, lastSyncBlockNum uint64) error {
	sqlQuery := `
	INSERT INTO meta(
		chainID,
		lastSyncBlockNum,
		lastUpdate
	) values(?, ?, CURRENT_TIMESTAMP)
	`

	stmt, err := r.db.Prepare(sqlQuery)
	if err != nil {
		return err
	}
	defer stmt.Close() //nolint:errcheck

	_, err = stmt.Exec(chainID, lastSyncBlockNum)
	if err != nil {
		return fmt.Errorf("InitMeta error: %s", err)
	}
	return nil
}

// UpdateLastSyncBlockNum stores the given lastSyncBlockNum into the meta
// unique row
func (r *SQLite) UpdateLastSyncBlockNum(lastSyncBlockNum uint64) error {
	sqlQuery := `
	UPDATE meta SET lastSyncBlockNum=? WHERE id=?
	`

	stmt, err := r.db.Prepare(sqlQuery)
	if err != nil {
		return fmt.Errorf("UpdateLastSyncBlockNum error: %s", err)
	}
	defer stmt.Close() //nolint:errcheck

	_, err = stmt.Exec(int(lastSyncBlockNum), 1)
	if err != nil {
		return fmt.Errorf("UpdateLastSyncBlockNum error: %s", err)
	}
	return nil
}

// GetLastSyncBlockNum gets the lastSyncBlockNum from the meta unique row
func (r *SQLite) GetLastSyncBlockNum() (uint64, error) {
	row := r.db.QueryRow("SELECT lastSyncBlockNum FROM meta WHERE id = 1")

	var lastSyncBlockNum int
	err := row.Scan(&lastSyncBlockNum)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, ErrMetaNotInDB
		}
		return 0, err
	}
	return uint64(lastSyncBlockNum), nil
}

// func (r *SQLite) ReadVotePackagesByCensusRoot(processID uint64) ([]types.VotePackage, error) {
// func (r *SQLite) ReadVoteByPublicKeyAndCensusRoot(censusRoot []byte) (
// 	[]types.VotePackage, error) {
// func (r *SQLite) ReadVotesByPublicKey(censusRoot []byte) ([]types.VotePackage, error) {
