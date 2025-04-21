// Package pgxtransactor provides utilities for managing PostgreSQL transactions with pgx.
// It simplifies transaction management with automatic rollback on errors and
// provides helper functions for working with transactions in a consistent way.
// Optional support for Squirrel query builder is included.
package pgxtransactor

import (
	"context"
	"errors"
	"fmt"

	sq "github.com/Masterminds/squirrel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	PgUniqueViolation      = "23505"
	PgForeignKeyViolation  = "23503"
	PgCheckViolation       = "23514"
	PgNotNullViolation     = "23502"
	PgSerializationFailure = "40001"
)

var (
	ErrTxCanceled = errors.New("transaction canceled")
	ErrTxConflict = errors.New("transaction conflict")
)

// TxOptions represents configurable transaction options.
type TxOptions struct {
	// IsolationLevel sets the transaction isolation level
	IsolationLevel pgx.TxIsoLevel

	// AccessMode determines if the transaction is read-write or read-only
	AccessMode pgx.TxAccessMode

	// DeferrableMode determines if constraints are checked at the end of the transaction
	DeferrableMode pgx.TxDeferrableMode
}

// Predefined transaction options for common scenarios
var (
	// DefaultTxOptions provides reasonable default transaction options.
	// Uses READ COMMITTED isolation level with read-write access.
	DefaultTxOptions = TxOptions{
		IsolationLevel: pgx.ReadCommitted,
		AccessMode:     pgx.ReadWrite,
		DeferrableMode: pgx.NotDeferrable,
	}

	// ReadOnlyTxOptions provides transaction options for read-only operations.
	// Uses READ COMMITTED isolation level with read-only access.
	ReadOnlyTxOptions = TxOptions{
		IsolationLevel: pgx.ReadCommitted,
		AccessMode:     pgx.ReadOnly,
		DeferrableMode: pgx.NotDeferrable,
	}

	// SerializableTxOptions provides transaction options for operations requiring
	// serializable isolation level.
	SerializableTxOptions = TxOptions{
		IsolationLevel: pgx.Serializable,
		AccessMode:     pgx.ReadWrite,
		DeferrableMode: pgx.NotDeferrable,
	}
)

// Querier is an interface for executing database queries.
// It's satisfied by both pgx.Tx and pgxpool.Pool, allowing flexible usage
// of the same repository methods with or without transactions.
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Storage represents a database storage layer component that can perform operations
// with a querier (either direct pool connection or transaction).
type Storage interface {
	WithQuerier(q Querier) Storage
	GetQuerier() Querier
	Builder() sq.StatementBuilderType
}

// BaseStorage provides a common implementation for storage components
// that need to work with transactions.
type BaseStorage struct {
	querier Querier
	// Squirrel StatementBuilder for query building
	builder sq.StatementBuilderType
}

// WithQuerier sets the querier for this storage and returns itself.
func (s *BaseStorage) WithQuerier(q Querier) Storage {
	s.querier = q
	return s
}

// GetQuerier returns the current querier (either pool or transaction).
func (s *BaseStorage) GetQuerier() Querier {
	return s.querier
}

// Builder returns the Squirrel StatementBuilder to use for building queries
func (s *BaseStorage) Builder() sq.StatementBuilderType {
	return s.builder
}

// NewBaseStorage creates a new BaseStorage with the given pool.
func NewBaseStorage(pool *pgxpool.Pool) *BaseStorage {
	return &BaseStorage{
		querier: pool,
		builder: sq.StatementBuilder.PlaceholderFormat(sq.Dollar),
	}
}

// Transactor provides methods for executing operations in a transaction.
type Transactor struct {
	pool *pgxpool.Pool
}

// NewTransactor creates a new Transactor with the given pgx connection pool.
func NewTransactor(pool *pgxpool.Pool) *Transactor {
	return &Transactor{
		pool: pool,
	}
}

// ExecuteInTx executes multiple storage operations within a single transaction.
// If any operation returns an error, the transaction is rolled back.
// The storages slice should contain all storage components that need to participate
// in the transaction.
//
// Example:
//
//	err := transactor.ExecuteInTx(ctx, []Storage{userRepo, orderRepo}, func() error {
//		if err := userRepo.Create(ctx, user); err != nil {
//			return err
//		}
//		return orderRepo.Create(ctx, order)
//	})
func (t *Transactor) ExecuteInTx(ctx context.Context, storages []Storage, fn func() error) error {
	return t.ExecuteInTxWithOptions(ctx, DefaultTxOptions, storages, fn)
}

// ExecuteInTxWithOptions executes multiple storage operations within a transaction with custom options.
// It automatically handles transaction rollback on error and provides proper error wrapping.
func (t *Transactor) ExecuteInTxWithOptions(ctx context.Context, opts TxOptions, storages []Storage, fn func() error) error {
	tx, err := t.pool.BeginTx(ctx, pgx.TxOptions{
		IsoLevel:       opts.IsolationLevel,
		AccessMode:     opts.AccessMode,
		DeferrableMode: opts.DeferrableMode,
	})
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	// Store original queriers to restore them later
	originalQueriers := make([]Querier, len(storages))
	for i, storage := range storages {
		originalQueriers[i] = storage.GetQuerier()
		storage.WithQuerier(tx)
	}

	// Restore original queriers after transaction
	defer func() {
		for i, storage := range storages {
			storage.WithQuerier(originalQueriers[i])
		}
	}()

	// Use a named return value to track errors for the defer function
	var txErr error
	defer func() {
		// If there was an error or panic, roll back the transaction
		if txErr != nil || recover() != nil {
			rbErr := tx.Rollback(ctx)
			if rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
				// Only log/append the rollback error if it's not because the tx was already closed
				txErr = fmt.Errorf("tx err: %v, rb err: %v", txErr, rbErr)
			}
		}
	}()

	// Execute the function within the transaction
	if txErr = fn(); txErr != nil {
		// If the error is due to cancellation, wrap it properly
		if IsCanceled(txErr) {
			return fmt.Errorf("%w: %v", ErrTxCanceled, txErr)
		}
		return txErr
	}

	// If everything was successful, commit the transaction
	if txErr = tx.Commit(ctx); txErr != nil {
		return fmt.Errorf("commit transaction: %w", txErr)
	}

	return nil
}

// ExecuteInReadOnlyTx executes operations in a read-only transaction.
func (t *Transactor) ExecuteInReadOnlyTx(ctx context.Context, storages []Storage, fn func() error) error {
	return t.ExecuteInTxWithOptions(ctx, ReadOnlyTxOptions, storages, fn)
}

// ExecuteInSerializableTx executes operations in a serializable transaction.
func (t *Transactor) ExecuteInSerializableTx(ctx context.Context, storages []Storage, fn func() error) error {
	return t.ExecuteInTxWithOptions(ctx, SerializableTxOptions, storages, fn)
}

// ExecuteWithRetry executes operations with automatic retry on serialization failures.
func (t *Transactor) ExecuteWithRetry(ctx context.Context, maxRetries int, storages []Storage, fn func() error) error {
	var err error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		err = t.ExecuteInTxWithOptions(ctx, SerializableTxOptions, storages, fn)

		// If there's no error or the error isn't a serialization failure, we're done
		if err == nil || !IsSerializationFailure(err) {
			return err
		}

		// If we've reached the maximum number of retries, return the error
		if attempt == maxRetries {
			return fmt.Errorf("%w: %v (after %d retries)", ErrTxConflict, err, maxRetries)
		}
	}

	return err // This should never be reached
}

// IsCanceled checks if an error was caused by context cancellation or deadline exceeded.
func IsCanceled(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

// IsUniqueViolation checks if an error was caused by a unique constraint violation.
func IsUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == PgUniqueViolation
}

// IsForeignKeyViolation checks if an error was caused by a foreign key constraint violation.
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == PgForeignKeyViolation
}

// IsCheckViolation checks if an error was caused by a check constraint violation.
func IsCheckViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == PgCheckViolation
}

// IsNotNullViolation checks if an error was caused by a not-null constraint violation.
func IsNotNullViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == PgNotNullViolation
}

// IsSerializationFailure checks if an error was caused by a serialization failure.
func IsSerializationFailure(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == PgSerializationFailure
}

// SquirrelHelper provides methods for executing squirrel SQL builders with pgx
type SquirrelHelper struct{}

// NewSquirrelHelper creates a new SquirrelHelper
func NewSquirrelHelper() *SquirrelHelper {
	return &SquirrelHelper{}
}

// Exec executes a Squirrel SQL builder with pgx's Exec method
func (h *SquirrelHelper) Exec(ctx context.Context, q Querier, builder sq.Sqlizer) (pgconn.CommandTag, error) {
	query, args, err := builder.ToSql()
	if err != nil {
		return pgconn.CommandTag{}, fmt.Errorf("build sql: %w", err)
	}
	return q.Exec(ctx, query, args...)
}

// Query executes a Squirrel SQL builder with pgx's Query method
func (h *SquirrelHelper) Query(ctx context.Context, q Querier, builder sq.Sqlizer) (pgx.Rows, error) {
	query, args, err := builder.ToSql()
	if err != nil {
		return nil, fmt.Errorf("build sql: %w", err)
	}
	return q.Query(ctx, query, args...)
}

// QueryRow executes a Squirrel SQL builder with pgx's QueryRow method
func (h *SquirrelHelper) QueryRow(ctx context.Context, q Querier, builder sq.Sqlizer) pgx.Row {
	query, args, err := builder.ToSql()
	if err != nil {
		return &pgxErrorRow{err: err}
	}
	return q.QueryRow(ctx, query, args...)
}

// pgxErrorRow is a helper type for returning an error from QueryRow
type pgxErrorRow struct {
	err error
}

// Scan implements pgx.Row interface for pgxErrorRow
func (r *pgxErrorRow) Scan(dest ...interface{}) error {
	return r.err
}
